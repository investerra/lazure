// Package azurearm models the Azure ARM Container App resource shape that
// Lazure both PUTs (to deploy) and GETs (to diff against the deployed
// state). Fields use ARM's native camelCase via json tags.
//
// Enum casing is NOT uniform in ARM — some enums are PascalCase
// (activeRevisionsMode: Single|Multiple; ipSecurityRestrictions.action:
// Allow|Deny; volume.storageType: EmptyDir|AzureFile|NfsAzureFile|Secret;
// probe.type: Liveness|Readiness|Startup; identity.type: UserAssigned|...)
// while others are lowercase (ingress.transport: auto|http|http2|tcp;
// stickySessions.affinity: none|sticky). Transform.go handles the split.
//
// This package only models fields Lazure writes. ARM GET responses carry
// additional read-only fields (etag, systemData, provisioningState,
// latestRevisionName, etc.) which JSON decoding silently drops — the
// diff normalizer in azureapi/diff.go handles read-only stripping
// explicitly for diff output.
package azurearm

// ContainerApp is the top-level ARM resource.
type ContainerApp struct {
	Type       string                 `json:"type"` // "Microsoft.App/containerApps"
	Location   string                 `json:"location"`
	Name       string                 `json:"name"`
	Identity   *Identity              `json:"identity,omitempty"`
	Properties ContainerAppProperties `json:"properties"`
}

// Identity configures the managed identity of the container app.
// For Lazure's single-user-assigned case: Type = "UserAssigned",
// UserAssignedIdentities is a map with exactly one entry whose key is the
// full identity resource id and whose value is an empty object ({}).
type Identity struct {
	Type                   string                        `json:"type"` // UserAssigned|SystemAssigned|SystemAssigned,UserAssigned|None
	UserAssignedIdentities map[string]UserAssignedDetail `json:"userAssignedIdentities,omitempty"`
}

// UserAssignedDetail is the per-identity payload. ARM expects an empty
// object on PUT; GET may populate principalId / clientId (read-only).
type UserAssignedDetail struct {
	PrincipalID string `json:"principalId,omitempty"`
	ClientID    string `json:"clientId,omitempty"`
}

type ContainerAppProperties struct {
	ManagedEnvironmentID string        `json:"managedEnvironmentId"`
	Configuration        Configuration `json:"configuration"`
	Template             Template      `json:"template"`

	// Read-only fields populated by GET responses. omitempty means we
	// never accidentally write them back in a PUT body — lazure-managed
	// state lives elsewhere in the struct.
	//
	// LatestRevisionName is the most recently *created* revision; it
	// may not yet be Ready. LatestReadyRevisionName is the latest one
	// that is actively serving traffic — they diverge while a fresh
	// revision is rolling out, or stay diverged when the new revision
	// fails to come up.
	LatestRevisionName      string `json:"latestRevisionName,omitempty"`
	LatestReadyRevisionName string `json:"latestReadyRevisionName,omitempty"`
	LatestRevisionFqdn      string `json:"latestRevisionFqdn,omitempty"`
	ProvisioningState       string `json:"provisioningState,omitempty"`
	RunningStatus           string `json:"runningStatus,omitempty"`
}

// Configuration groups app-level (non-replica) settings. Secrets here are
// auto-populated from deploy.yml secret references via transform.go.
type Configuration struct {
	ActiveRevisionsMode string     `json:"activeRevisionsMode,omitempty"` // Single | Multiple
	Ingress             *Ingress   `json:"ingress,omitempty"`
	Registries          []Registry `json:"registries,omitempty"`
	Secrets             []Secret   `json:"secrets,omitempty"`
}

type Ingress struct {
	External               bool                    `json:"external"`
	TargetPort             int                     `json:"targetPort"`
	Transport              string                  `json:"transport,omitempty"`     // lowercase: auto | http | http2 | tcp
	AllowInsecure          bool                    `json:"allowInsecure,omitempty"`
	CorsPolicy             *CorsPolicy             `json:"corsPolicy,omitempty"`
	IPSecurityRestrictions []IPSecurityRestriction `json:"ipSecurityRestrictions,omitempty"`
	Traffic                []TrafficEntry          `json:"traffic,omitempty"`

	// FQDN is read-only on GET — Azure publishes the assigned hostname
	// once the app has ingress. omitempty so we never write it on PUT.
	FQDN string `json:"fqdn,omitempty"`
}

type CorsPolicy struct {
	AllowCredentials bool     `json:"allowCredentials,omitempty"`
	AllowedHeaders   []string `json:"allowedHeaders,omitempty"`
	AllowedMethods   []string `json:"allowedMethods,omitempty"`
	AllowedOrigins   []string `json:"allowedOrigins,omitempty"`
	ExposeHeaders    []string `json:"exposeHeaders,omitempty"`
	MaxAge           int      `json:"maxAge,omitempty"`
}

type IPSecurityRestriction struct {
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	IPAddressRange string `json:"ipAddressRange"`
	Action         string `json:"action"` // Allow | Deny (capitalized)
}

// TrafficEntry — each entry points at either a specific revision name or
// the always-latest one. Weight is a percentage; all entries sum to 100.
type TrafficEntry struct {
	Weight         int    `json:"weight"`
	LatestRevision bool   `json:"latestRevision,omitempty"`
	RevisionName   string `json:"revisionName,omitempty"`
	Label          string `json:"label,omitempty"`
}

// Registry — one image-pull source. Exactly one auth mode applies: either
// Identity (managed identity to ACR) or Username+PasswordSecretRef (basic
// auth against any Docker registry; the secret lives in Configuration.Secrets).
type Registry struct {
	Server            string `json:"server"`
	Identity          string `json:"identity,omitempty"`
	Username          string `json:"username,omitempty"`
	PasswordSecretRef string `json:"passwordSecretRef,omitempty"`
}

// Secret — Lazure always uses Key Vault–backed secrets. Transform.go emits
// one entry per referenced secret name with KeyVaultURL + Identity filled in
// from the SOPS metadata vault URL and App.Identity respectively.
//
// ARM also supports Value (plaintext) which Lazure never writes.
type Secret struct {
	Name        string `json:"name"`
	KeyVaultURL string `json:"keyVaultUrl,omitempty"`
	Identity    string `json:"identity,omitempty"`
	Value       string `json:"value,omitempty"`
}

// Template groups per-replica (pod-shaped) settings: containers, init
// containers, shared volumes, and scale bounds.
type Template struct {
	Containers     []Container `json:"containers,omitempty"`
	InitContainers []Container `json:"initContainers,omitempty"`
	Volumes        []Volume    `json:"volumes,omitempty"`
	Scale          *Scale      `json:"scale,omitempty"`
}

type Container struct {
	Name         string        `json:"name"`
	Image        string        `json:"image,omitempty"`
	Command      []string      `json:"command,omitempty"`
	Args         []string      `json:"args,omitempty"`
	WorkingDir   string        `json:"workingDir,omitempty"`
	Env          []EnvVar      `json:"env,omitempty"`
	Resources    *Resources    `json:"resources,omitempty"`
	VolumeMounts []VolumeMount `json:"volumeMounts,omitempty"`
	Probes       []Probe       `json:"probes,omitempty"`
}

// EnvVar — ARM uses an array (not a map) and each entry has exactly one of
// Value or SecretRef set.
type EnvVar struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	SecretRef string `json:"secretRef,omitempty"`
}

type Resources struct {
	CPU    float64 `json:"cpu,omitempty"`
	Memory string  `json:"memory,omitempty"`
}

type VolumeMount struct {
	VolumeName string `json:"volumeName"`
	MountPath  string `json:"mountPath"`
	SubPath    string `json:"subPath,omitempty"`
}

// Probe — ARM wraps probes as an array tagged by Type rather than a
// liveness/readiness/startup map. ACA supports exactly TWO probe action
// types: HTTPGet and TCPSocket. There is no exec probe in ACA (unlike
// Kubernetes).
type Probe struct {
	Type                          string      `json:"type"` // Liveness | Readiness | Startup
	HTTPGet                       *HTTPAction `json:"httpGet,omitempty"`
	TCPSocket                     *TCPAction  `json:"tcpSocket,omitempty"`
	InitialDelaySeconds           int         `json:"initialDelaySeconds,omitempty"`
	PeriodSeconds                 int         `json:"periodSeconds,omitempty"`
	TimeoutSeconds                int         `json:"timeoutSeconds,omitempty"`
	FailureThreshold              int         `json:"failureThreshold,omitempty"`
	SuccessThreshold              int         `json:"successThreshold,omitempty"`
	TerminationGracePeriodSeconds int         `json:"terminationGracePeriodSeconds,omitempty"`
}

type HTTPAction struct {
	Host        string       `json:"host,omitempty"`
	HTTPHeaders []HTTPHeader `json:"httpHeaders,omitempty"`
	Path        string       `json:"path,omitempty"`
	Port        int          `json:"port"`
	Scheme      string       `json:"scheme,omitempty"` // HTTP | HTTPS
}

type HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type TCPAction struct {
	Host string `json:"host,omitempty"`
	Port int    `json:"port"`
}

// Volume — a template-wide volume definition. StorageType values are
// capitalized per ARM: EmptyDir | AzureFile | NfsAzureFile | Secret.
type Volume struct {
	Name         string `json:"name"`
	StorageType  string `json:"storageType"`
	StorageName  string `json:"storageName,omitempty"`
	MountOptions string `json:"mountOptions,omitempty"`
}

// Scale — replica bounds and KEDA-style autoscaling rules.
type Scale struct {
	MinReplicas     int         `json:"minReplicas,omitempty"`
	MaxReplicas     int         `json:"maxReplicas,omitempty"`
	CooldownPeriod  int         `json:"cooldownPeriod,omitempty"`
	PollingInterval int         `json:"pollingInterval,omitempty"`
	Rules           []ScaleRule `json:"rules,omitempty"`
}

// ScaleRule — ARM exposes four first-class scaler shapes (http, tcp,
// azureQueue, custom). CPU and memory scalers from our lazurecfg schema
// are transformed to Custom entries with Type="cpu"/"memory".
//
// Workload identity (for e.g. Azure Queue auth) is set on each scaler
// sub-struct — NOT on ScaleRule itself. Same for auth. ARM's shape is
// quirky this way.
type ScaleRule struct {
	Name       string           `json:"name"`
	HTTP       *HTTPScaleRule   `json:"http,omitempty"`
	TCP        *TCPScaleRule    `json:"tcp,omitempty"`
	AzureQueue *QueueScaleRule  `json:"azureQueue,omitempty"`
	Custom     *CustomScaleRule `json:"custom,omitempty"`
}

// KEDA scaler metadata is string-valued in ARM (e.g. concurrentRequests:
// "10", not 10). Transform.go stringifies numbers as needed.
type HTTPScaleRule struct {
	Identity string            `json:"identity,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Auth     []ScaleRuleAuth   `json:"auth,omitempty"`
}

type TCPScaleRule struct {
	Identity string            `json:"identity,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Auth     []ScaleRuleAuth   `json:"auth,omitempty"`
}

type QueueScaleRule struct {
	AccountName string          `json:"accountName,omitempty"`
	Identity    string          `json:"identity,omitempty"`
	QueueLength int             `json:"queueLength,omitempty"`
	QueueName   string          `json:"queueName,omitempty"`
	Auth        []ScaleRuleAuth `json:"auth,omitempty"`
}

type CustomScaleRule struct {
	Identity string            `json:"identity,omitempty"`
	Type     string            `json:"type"` // KEDA scaler name: cpu, memory, redis, kafka, ...
	Metadata map[string]string `json:"metadata,omitempty"`
	Auth     []ScaleRuleAuth   `json:"auth,omitempty"`
}

type ScaleRuleAuth struct {
	TriggerParameter string `json:"triggerParameter"`
	SecretRef        string `json:"secretRef"`
}
