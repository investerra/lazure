package azureapi

import (
	"reflect"
	"testing"
)

func TestUnsupportedLiveStateFields_DetectsFieldsDeployWouldDrop(t *testing.T) {
	raw := map[string]any{
		"tags": map[string]any{"owner": "platform"},
		"properties": map[string]any{
			"configuration": map[string]any{
				"dapr": map[string]any{
					"enabled": true,
					"appId":   "kyc",
				},
				"ingress": map[string]any{
					"additionalPortMappings": []any{
						map[string]any{"targetPort": float64(9000), "external": true},
					},
					"clientCertificateMode": "require",
					"stickySessions": map[string]any{
						"affinity": "sticky",
					},
				},
			},
			"template": map[string]any{
				"revisionSuffix": "manual",
				"serviceBinds": []any{
					map[string]any{"name": "redis", "serviceId": "/services/redis"},
				},
			},
		},
	}

	got := UnsupportedLiveStateFields(raw)
	want := []string{
		"/properties/configuration/dapr",
		"/properties/configuration/ingress/additionalPortMappings",
		"/properties/configuration/ingress/clientCertificateMode",
		"/properties/configuration/ingress/stickySessions",
		"/properties/template/revisionSuffix",
		"/properties/template/serviceBinds",
		"/tags",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UnsupportedLiveStateFields() = %#v, want %#v", got, want)
	}
}

func TestUnsupportedLiveStateFields_IgnoresPreservedAndReadOnlyFields(t *testing.T) {
	raw := map[string]any{
		"id":         "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/containerApps/kyc",
		"type":       "Microsoft.App/containerApps",
		"systemData": map[string]any{"createdBy": "alex"},
		"properties": map[string]any{
			"provisioningState": "Succeeded",
			"configuration": map[string]any{
				"ingress": map[string]any{
					"customDomains": []any{
						map[string]any{"name": "api.example.com"},
					},
					"fqdn":                  "kyc.example.azurecontainerapps.io",
					"clientCertificateMode": "ignore",
					"stickySessions":        map[string]any{"affinity": "none"},
				},
				"dapr":                 map[string]any{"enabled": false},
				"maxInactiveRevisions": float64(0),
			},
		},
	}

	if got := UnsupportedLiveStateFields(raw); len(got) != 0 {
		t.Fatalf("UnsupportedLiveStateFields() = %#v, want none", got)
	}
}
