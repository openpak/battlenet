// Identity for the Battle.net tier, resolved through the account core.
//
// check_token receives the id_token of the caller's switch sub-account (the
// openpak claim names its subject) and must answer with the caller's battlenet
// sub-account. Both are adapter links on the same core account, so the
// exchange is two lookups and, on first login, one provisioning: the
// battlenet subject is minted here because this adapter is the authority for
// its namespace — the same authority nx-baas is for "switch" (SW-01).
//
// The id_token's signature is verified against the nx-baas JWKS (the token's
// own jku/kid name it: openpak-baas-key-1). On the redirected network this
// request can only originate from a console the adapter armed, but the
// signature check is what turns that assumption into a property; the claim
// checks below bound what a forged token could assert even so.
package main

import (
	"context"
	"crypto"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	accountv1 "openpak/battlenet/accountpb"
)

const (
	switchNamespace    = "switch"
	battlenetNamespace = "battlenet"
)

type identityClient struct {
	links    accountv1.LinksClient
	accounts accountv1.IdentityClient
	conn     *grpc.ClientConn
	addr     string
	key      string
}

func newIdentityClient(ctx context.Context, addr, internalKey string) (*identityClient, error) {
	if len(internalKey) < 32 {
		return nil, errors.New("ACCOUNT_INTERNAL_KEY must be set (>=32 bytes)")
	}

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// The core enforces internal credentials per call; the key names this
		// node the same way it names nx-baas.
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any,
			cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			return invoker(metadata.AppendToOutgoingContext(ctx, "x-internal-key", internalKey),
				method, req, reply, cc, opts...)
		}))
	if err != nil {
		return nil, err
	}

	// The warmup answers whether the core is reachable at all: NotFound is a
	// healthy answer to a subject that does not exist. Anything else is
	// logged and tolerated — the service still serves geo and telemetry, and
	// check_token retries the connection on demand.
	warm, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, warmErr := accountv1.NewLinksClient(conn).GetLinkBySubject(warm, &accountv1.GetLinkBySubjectRequest{
		Namespace: battlenetNamespace, SubjectId: "warmup"})
	if warmErr != nil {
		log.Printf("identity warmup: core answered %v (continuing; check_token will retry)", warmErr)
	}

	return &identityClient{links: accountv1.NewLinksClient(conn), accounts: accountv1.NewIdentityClient(conn),
		conn: conn, addr: addr, key: internalKey}, nil
}

// reconnect drops the pooled connection and dials fresh. The account core's
// container address changes whenever that service is recreated, and the
// pooled gRPC connection keeps dialing the old one; every login then fails
// with no route to host until this process restarts.
func (c *identityClient) reconnect(ctx context.Context) error {
	_ = c.conn.Close()

	conn, err := grpc.NewClient(c.addr, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any,
			cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			return invoker(metadata.AppendToOutgoingContext(ctx, "x-internal-key", c.key),
				method, req, reply, cc, opts...)
		}))
	if err != nil {
		return err
	}

	c.conn = conn
	c.links = accountv1.NewLinksClient(conn)
	c.accounts = accountv1.NewIdentityClient(conn)

	log.Printf("identity client reconnected to %s", c.addr)

	return nil
}

// resolve runs the identity exchange once, and once more on a fresh
// connection when the first attempt died dialing a stale address.
func (c *identityClient) resolve(ctx context.Context, idToken string) (string, error) {
	subject, err := c.resolveOnce(ctx, idToken)
	if err == nil {
		return subject, nil
	}
	if status.Code(err) != codes.Unavailable {
		return subject, err
	}

	log.Printf("identity connection unavailable; reconnecting")

	if reconnectErr := c.reconnect(ctx); reconnectErr != nil {
		return "", fmt.Errorf("%w (reconnect failed: %v)", err, reconnectErr)
	}

	return c.resolveOnce(ctx, idToken)
}

func (c *identityClient) Close() { _ = c.conn.Close() }

var errUnauthenticated = errors.New("unauthenticated")

// errBanned is an account the admin banned, or one being deleted
// (website/docs/ban-lookup.md). Checked at every login, straight from the core:
// no session is issued for it.
var errBanned = fmt.Errorf("%w: account banned", errUnauthenticated)

// accountAllowed asks the core whether the account may sign in. Only ACTIVE may;
// an unknown account is locked out too, and a core that cannot answer refuses the
// login (the caller retries once on a fresh connection when it is Unavailable).
func accountAllowed(ctx context.Context, accounts accountv1.IdentityClient, accountID string) error {
	acct, err := accounts.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: accountID})
	if status.Code(err) == codes.NotFound {
		return errBanned
	}
	if err != nil {
		return fmt.Errorf("account status lookup: %w", err)
	}
	if acct.GetStatus() != accountv1.AccountStatus_ACCOUNT_STATUS_ACTIVE {
		return errBanned
	}
	return nil
}

// switchSubjectFromToken parses the id_token payload and returns the switch
// link subject it carries: the BAAS "sub", which is what the core's switch
// namespace keys on.
func switchSubjectFromToken(idToken string) (string, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("%w: id_token is not a JWT", errUnauthenticated)
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("%w: id_token payload: %v", errUnauthenticated, err)
	}

	var claims struct {
		Iss string `json:"iss"`
		Sub string `json:"sub"`
		Typ string `json:"typ"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("%w: id_token claims: %v", errUnauthenticated, err)
	}
	if !strings.Contains(claims.Iss, "baas.nintendo.com") || claims.Typ != "id_token" {
		return "", fmt.Errorf("%w: not a BAAS id_token", errUnauthenticated)
	}
	// Expired tokens are refused with a minute of slack for clock skew
	// between the console host and this service.
	if claims.Exp > 0 && time.Now().Unix() > claims.Exp+60 {
		return "", fmt.Errorf("%w: id_token expired", errUnauthenticated)
	}
	// The switch link subject is the BAAS "sub" (a hex subject id) — the same
	// value nx-baas stores when the console links. The openpak claim's decimal
	// uid is the BAAS-facing id and matches nothing in adapter_links.
	if claims.Sub == "" {
		return "", fmt.Errorf("%w: id_token carries no subject", errUnauthenticated)
	}

	return claims.Sub, nil
}

// jwksCache holds the nx-baas signing keys. Fetched on first use, refreshed
// after ten minutes or when an unknown kid shows up (key rotation) — login is
// rare enough that laziness beats a refresh goroutine here.
type jwksCache struct {
	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

const (
	// The plain-HTTP listener inside the pod network; the Host header is what
	// routes the request to the BAAS surface on the shared handler.
	defaultJWKSURL  = "http://openpak-nx-baas:21080/1.0.0/certificates"
	defaultJWKSHost = "m-lp1.baas.nintendo.com"
	jwksMaxAge      = 10 * time.Minute
)

// publicKey returns the key for kid, fetching or refreshing the JWKS as
// needed. A kid missing from a fresh cache is an error; a fetch failure is
// fail-closed — nx-baas is core infrastructure, and if it is unreachable the
// login should say so rather than accept unverified tokens.
func (j *jwksCache) publicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	fetch := j.keys == nil || time.Since(j.fetched) > jwksMaxAge
	if !fetch {
		if k, ok := j.keys[kid]; ok {
			return k, nil
		}
		fetch = true // unknown kid: refresh once in case the key just rotated
	}
	if fetch {
		keys, err := fetchJWKS(ctx, env("BAAS_JWKS_URL", defaultJWKSURL), env("BAAS_JWKS_HOST", defaultJWKSHost))
		if err != nil {
			if k, ok := j.keys[kid]; ok {
				return k, nil // stale keys beat no keys, but the error is logged
			}
			return nil, fmt.Errorf("JWKS fetch: %w", err)
		}
		j.keys, j.fetched = keys, time.Now()
	}
	if k, ok := j.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("kid %q not in nx-baas JWKS", kid)
}

func fetchJWKS(ctx context.Context, url, host string) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Host = host

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS %d: %s", resp.StatusCode, body)
	}

	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		}
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("JWKS body: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if pub.E < 2 || pub.E > math.MaxInt32 {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("JWKS carried no usable keys")
	}
	return keys, nil
}

// verifySignature checks the RS256 signature of the id_token against the
// nx-baas JWKS key named by its kid. The issuer is our own BAAS, so its key
// is the only authority a token on this network can carry.
func (c *identityClient) verifySignature(ctx context.Context, idToken string) error {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return errors.New("id_token is not a JWT")
	}

	head, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("id_token header: %w", err)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(head, &hdr); err != nil {
		return fmt.Errorf("id_token header: %w", err)
	}
	if hdr.Alg != "RS256" {
		return fmt.Errorf("id_token alg %q, want RS256", hdr.Alg)
	}
	pub, err := jwks.publicKey(ctx, hdr.Kid)
	if err != nil {
		return err
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("id_token signature: %w", err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return fmt.Errorf("id_token signature: %w", err)
	}
	return nil
}

// jwks is the process-wide key cache; identity requests are the only user.
var jwks = &jwksCache{}

// battleNetSubject returns the caller's battlenet link subject, minting and
// activating it on first login. The two-step reserve/activate mirrors
// nx-baas's ensure (SW-01): retries never mint a second subject because
// GetActiveLinkByAccount is checked first.
func (c *identityClient) battleNetSubject(ctx context.Context, accountID string) (string, error) {
	got, err := c.links.GetActiveLinkByAccount(ctx, &accountv1.GetActiveLinkByAccountRequest{
		Namespace: battlenetNamespace, AccountId: accountID})
	if err == nil {
		return got.GetSubjectId(), nil
	}
	if status.Code(err) != codes.NotFound {
		return "", fmt.Errorf("battlenet link lookup: %w", err)
	}

	subject := newBattleNetSubject()
	res, err := c.links.ReserveLink(ctx, &accountv1.ReserveLinkRequest{
		AccountId: accountID, Namespace: battlenetNamespace, SubjectId: subject,
		IdempotencyKey: "battlenet-first-login:" + accountID})
	if err != nil {
		return "", fmt.Errorf("battlenet link reserve: %w", err)
	}
	if _, err := c.links.ActivateLink(ctx, &accountv1.ActivateLinkRequest{
		LinkId: res.GetLinkId(), IdempotencyKey: "battlenet-first-login:" + accountID}); err != nil {
		return "", fmt.Errorf("battlenet link activate: %w", err)
	}

	return subject, nil
}

// resolveOnce is the whole check_token identity step: signature, switch
// subject from the token, core account from the switch link, battlenet
// subject from the account.
func (c *identityClient) resolveOnce(ctx context.Context, idToken string) (string, error) {
	if err := c.verifySignature(ctx, idToken); err != nil {
		return "", fmt.Errorf("%w: %v", errUnauthenticated, err)
	}

	switchSubject, err := switchSubjectFromToken(idToken)
	if err != nil {
		return "", err
	}

	link, err := c.links.GetLinkBySubject(ctx, &accountv1.GetLinkBySubjectRequest{
		Namespace: switchNamespace, SubjectId: switchSubject})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return "", fmt.Errorf("%w: no switch link for subject", errUnauthenticated)
		}
		return "", fmt.Errorf("switch link lookup: %w", err)
	}
	if link.GetState() != accountv1.LinkState_LINK_STATE_ACTIVE {
		return "", fmt.Errorf("%w: switch link is %s", errUnauthenticated, link.GetState())
	}
	if err := accountAllowed(ctx, c.accounts, link.GetAccountId()); err != nil {
		return "", err
	}

	return c.battleNetSubject(ctx, link.GetAccountId())
}

// newBattleNetSubject mints a Battle.net-shaped subject: a decimal uid in the
// range real uids occupy, kept well clear of the switch namespace's space.
func newBattleNetSubject() string {
	n, err := crand.Int(crand.Reader, big.NewInt(4_000_000_000))
	if err != nil {
		panic(fmt.Sprintf("battlenet: subject entropy: %v", err))
	}
	return fmt.Sprintf("%d", 1_000_000_000+n.Int64())
}
