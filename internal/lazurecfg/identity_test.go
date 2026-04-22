package lazurecfg

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIdentity_UnmarshalJSON_String(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  Identity
	}{
		{
			name:  "full resource id",
			input: `"/subscriptions/aaaa-bbbb/resourceGroups/rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/x"`,
			want:  "/subscriptions/aaaa-bbbb/resourceGroups/rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/x",
		},
		{
			name:  "empty string",
			input: `""`,
			want:  "",
		},
		{
			name:  "plain string",
			input: `"something"`,
			want:  "something",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var id Identity
			if err := json.Unmarshal([]byte(tc.input), &id); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tc.want {
				t.Errorf("got %q, want %q", id, tc.want)
			}
		})
	}
}

func TestIdentity_UnmarshalJSON_Errors(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantSubstr string
	}{
		{"object", `{"type":"UserAssigned"}`, "object shape is not yet supported"},
		{"empty object", `{}`, "object shape is not yet supported"},
		{"number", `42`, "expected string"},
		{"bool", `true`, "expected string"},
		{"array", `["a","b"]`, "expected string"},
		{"null", `null`, "expected string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var id Identity
			err := json.Unmarshal([]byte(tc.input), &id)
			if err == nil {
				t.Fatalf("expected error, got Identity(%q)", id)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

func TestIdentity_SubscriptionID(t *testing.T) {
	cases := []struct {
		name string
		id   Identity
		want string
	}{
		{
			name: "full UAMI resource id",
			id:   "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-example-dev/providers/Microsoft.ManagedIdentity/userAssignedIdentities/app-identity",
			want: "00000000-0000-0000-0000-000000000000",
		},
		{
			name: "subscription only",
			id:   "/subscriptions/abc-123",
			want: "abc-123",
		},
		{
			name: "subscription only with trailing slash",
			id:   "/subscriptions/abc-123/",
			want: "abc-123",
		},
		{
			name: "no leading slash",
			id:   "subscriptions/abc-123/resourceGroups/rg",
			want: "abc-123",
		},
		{
			name: "empty",
			id:   "",
			want: "",
		},
		{
			name: "garbage",
			id:   "/not-a-resource-id/foo",
			want: "",
		},
		{
			name: "missing subscription segment",
			id:   "/subscriptions",
			want: "",
		},
		{
			name: "wrong prefix",
			id:   "/resourceGroups/rg",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.id.SubscriptionID(); got != tc.want {
				t.Errorf("SubscriptionID() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestIdentity_InStruct verifies the identity decodes correctly when nested
// inside a larger struct (the real use case via App / Registry).
func TestIdentity_InStruct(t *testing.T) {
	type wrapper struct {
		Identity Identity `json:"identity"`
	}
	input := `{"identity":"/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/x"}`
	var w wrapper
	if err := json.Unmarshal([]byte(input), &w); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Identity.SubscriptionID() != "sub-1" {
		t.Errorf("SubscriptionID() = %q, want %q", w.Identity.SubscriptionID(), "sub-1")
	}
}
