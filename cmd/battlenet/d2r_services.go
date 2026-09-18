package main

import (
	"encoding/hex"
	"log"
)

// handleD2RServices serves the D2R-services WebSocket the title opens at
// LoginResponse.field1 + "/OSI/v2/rpc/client" once it goes online. Its first
// call is D2rServicesConnection.AuthSession (classic.protocol Header + an
// AuthSessionRequest whose field 1 echoes LoginResponse field 2).
func handleD2RServices(s *gatewaySession, payload []byte) bool {
	log.Printf("D2RSVC %s rpc in (%d bytes): %s", s.remote, len(payload), hex.EncodeToString(payload))
	return true
}
