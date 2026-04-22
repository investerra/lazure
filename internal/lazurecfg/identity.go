package lazurecfg

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Identity is a full UserAssigned managed-identity resource id of the form
//
//	/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.ManagedIdentity/userAssignedIdentities/{name}
//
// It is stored as a plain string so the common case (single identity) stays
// cheap. The typed alias exists so a custom UnmarshalJSON can reject
// accidentally-nested object inputs with a clear error — today the schema
// only supports a single user-assigned identity; object shapes for
// multi-identity / SystemAssigned are deferred.
type Identity string

// UnmarshalJSON decodes a JSON string into the Identity. Object inputs are
// rejected with a message pointing at the deferred multi-identity work; any
// other shape is an error.
func (i *Identity) UnmarshalJSON(data []byte) error {
	switch firstNonSpace(data) {
	case '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("identity: decoding string: %w", err)
		}
		*i = Identity(s)
		return nil
	case '{':
		return fmt.Errorf("identity: object shape is not yet supported; provide a single user-assigned identity resource id as a string")
	default:
		return fmt.Errorf("identity: expected string, got %s", string(data))
	}
}

// SubscriptionID extracts the subscription GUID from the identity resource
// id. Returns an empty string if the id doesn't match the expected
// /subscriptions/{sub}/... prefix.
func (i Identity) SubscriptionID() string {
	s := string(i)
	s = strings.TrimPrefix(s, "/")
	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 2 || parts[0] != "subscriptions" {
		return ""
	}
	return parts[1]
}
