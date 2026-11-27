package handler

import (
	"strings"
	"testing"
)

func TestNodeUpdateRejectedFieldsDoNotChangeAgentProtocols(t *testing.T) {
	for _, tc := range []struct {
		name string
		bad  map[string]interface{}
	}{
		{"invalid port", map[string]interface{}{"port": "70000"}},
		{"empty name", map[string]interface{}{"name": ""}},
		{"oversized interface", map[string]interface{}{"interfaceName": strings.Repeat("i", 201)}},
		{"duplicate name and address", map[string]interface{}{"name": "offline"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newNodeSwapFakeAgent(t)
			request := map[string]interface{}{"id": int64(1), "http": 1}
			for key, value := range tc.bad {
				request[key] = value
			}
			if result := kr1CallNodeUpdate(t, a.h, request); result.Code == 0 {
				t.Fatal("invalid node fields must reject the update")
			}
			if commands := a.commandsOfType("SetProtocol"); len(commands) != 0 {
				t.Errorf("rejected update changed the live agent protocol before validation: %+v", commands)
			}
			node, err := a.h.repo.GetNodeByID(1)
			if err != nil || node == nil || node.HTTP != 0 {
				t.Fatalf("rejected update changed persisted protocol: node=%+v err=%v", node, err)
			}
		})
	}
}
