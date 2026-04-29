package azureapi

import "sort"

type liveFieldRule struct {
	path       string
	meaningful func(any) bool
}

var unsupportedLiveFieldRules = []liveFieldRule{
	{path: "/extendedLocation", meaningful: meaningfulJSONValue},
	{path: "/managedBy", meaningful: meaningfulJSONValue},
	{path: "/tags", meaningful: meaningfulJSONValue},
	{path: "/properties/configuration/dapr", meaningful: meaningfulDapr},
	{path: "/properties/configuration/identitySettings", meaningful: meaningfulJSONValue},
	{path: "/properties/configuration/maxInactiveRevisions", meaningful: meaningfulJSONValue},
	{path: "/properties/configuration/runtime", meaningful: meaningfulJSONValue},
	{path: "/properties/configuration/service", meaningful: meaningfulJSONValue},
	{path: "/properties/configuration/ingress/additionalPortMappings", meaningful: meaningfulJSONValue},
	{path: "/properties/configuration/ingress/clientCertificateMode", meaningful: meaningfulClientCertificateMode},
	{path: "/properties/configuration/ingress/exposedPort", meaningful: meaningfulJSONValue},
	{path: "/properties/configuration/ingress/stickySessions", meaningful: meaningfulStickySessions},
	{path: "/properties/template/revisionSuffix", meaningful: meaningfulJSONValue},
	{path: "/properties/template/serviceBinds", meaningful: meaningfulJSONValue},
	{path: "/properties/template/terminationGracePeriodSeconds", meaningful: meaningfulJSONValue},
	{path: "/properties/workloadProfileName", meaningful: meaningfulJSONValue},
}

type ContainerAppFieldMapping struct {
	Managed              []string `json:"managed"`
	PreservedExternal    []string `json:"preserved_external"`
	Ignored              []string `json:"ignored"`
	NormalizedDefaults   []string `json:"normalized_defaults"`
	UnsupportedLiveState []string `json:"unsupported_live_state"`
}

func ContainerAppFieldMappingRules() ContainerAppFieldMapping {
	unsupported := make([]string, 0, len(unsupportedLiveFieldRules))
	for _, rule := range unsupportedLiveFieldRules {
		unsupported = append(unsupported, rule.path)
	}
	sort.Strings(unsupported)
	return ContainerAppFieldMapping{
		Managed: []string{
			"/identity",
			"/location",
			"/properties/managedEnvironmentId",
			"/properties/configuration/activeRevisionsMode",
			"/properties/configuration/ingress/external",
			"/properties/configuration/ingress/targetPort",
			"/properties/configuration/ingress/transport",
			"/properties/configuration/ingress/allowInsecure",
			"/properties/configuration/ingress/corsPolicy",
			"/properties/configuration/ingress/ipSecurityRestrictions",
			"/properties/configuration/ingress/traffic",
			"/properties/configuration/registries",
			"/properties/configuration/secrets",
			"/properties/template/containers",
			"/properties/template/initContainers",
			"/properties/template/scale",
			"/properties/template/volumes",
		},
		PreservedExternal: []string{
			"/properties/configuration/ingress/customDomains",
		},
		Ignored: []string{
			"/id",
			"/name",
			"/type",
			"/systemData",
			"/properties/latestRevisionName",
			"/properties/latestReadyRevisionName",
			"/properties/latestRevisionFqdn",
			"/properties/provisioningState",
			"/properties/runningStatus",
			"/properties/configuration/ingress/fqdn",
			"/properties/template/containers/env[name=LAZURE_FORCE_REDEPLOYED_AT]",
		},
		NormalizedDefaults: []string{
			"/identity/userAssignedIdentities keys are canonicalized as case-insensitive ARM IDs",
			"/location display names are canonicalized to Azure location names",
			"/properties/configuration/registries/identity is canonicalized as case-insensitive ARM ID",
			"/properties/configuration/secrets/identity is canonicalized as case-insensitive ARM ID",
			"/properties/configuration/ingress/transport=auto",
			"/properties/configuration/ingress/traffic=[{latestRevision:true,weight:100}]",
		},
		UnsupportedLiveState: unsupported,
	}
}

// UnsupportedLiveStateFields returns live Container App fields that Lazure
// does not manage or preserve, but a full deploy PUT would remove.
func UnsupportedLiveStateFields(raw map[string]any) []string {
	if raw == nil {
		return nil
	}
	var fields []string
	for _, rule := range unsupportedLiveFieldRules {
		v, ok := jsonPathValue(raw, rule.path)
		if !ok {
			continue
		}
		if rule.meaningful(v) {
			fields = append(fields, rule.path)
		}
	}
	sort.Strings(fields)
	return fields
}

func jsonPathValue(raw map[string]any, path string) (any, bool) {
	var cur any = raw
	for _, part := range splitJSONPointer(path) {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func splitJSONPointer(path string) []string {
	if path == "" || path == "/" {
		return nil
	}
	if path[0] == '/' {
		path = path[1:]
	}
	var parts []string
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '/' {
			parts = append(parts, path[start:i])
			start = i + 1
		}
	}
	return parts
}

func meaningfulJSONValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case bool:
		return x
	case float64:
		return x != 0
	case map[string]any:
		for _, vv := range x {
			if meaningfulJSONValue(vv) {
				return true
			}
		}
		return false
	case []any:
		for _, vv := range x {
			if meaningfulJSONValue(vv) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func meaningfulDapr(v any) bool {
	obj, ok := v.(map[string]any)
	if !ok {
		return meaningfulJSONValue(v)
	}
	for k, vv := range obj {
		if k == "enabled" {
			if enabled, ok := vv.(bool); ok && !enabled {
				continue
			}
		}
		if meaningfulJSONValue(vv) {
			return true
		}
	}
	return false
}

func meaningfulClientCertificateMode(v any) bool {
	s, ok := v.(string)
	if !ok {
		return meaningfulJSONValue(v)
	}
	return s != "" && s != "ignore" && s != "Ignore"
}

func meaningfulStickySessions(v any) bool {
	obj, ok := v.(map[string]any)
	if !ok {
		return meaningfulJSONValue(v)
	}
	affinity, _ := obj["affinity"].(string)
	return affinity != "" && affinity != "none"
}
