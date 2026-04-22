// Package lazurecfg holds the Lazure YAML schema — the types that users write
// in lazure.yml, envs/{env}.vars.yml, and envs/{env}.secrets.yml.
//
// The schema maps to Azure Container Apps ARM at transform time; see the
// azurearm package for the output model.
package lazurecfg

// Manifest is the top-level shape of lazure.yml.
type Manifest struct {
	App            App                  `json:"app"`
	Ingress        *Ingress             `json:"ingress,omitempty"`
	Registries     []Registry           `json:"registries,omitempty"`
	Scale          *Scale               `json:"scale,omitempty"`
	Volumes        []Volume             `json:"volumes,omitempty"`
	Env            map[string]*EnvValue `json:"env,omitempty"`
	InitContainers []Container          `json:"init_containers,omitempty"`
	Containers     []Container          `json:"containers"`
}

// App is the identity/targeting header of the manifest.
type App struct {
	Name                 string   `json:"name"`
	Location             string   `json:"location"`
	ResourceGroup        string   `json:"resource_group"`
	ManagedEnvironmentID string   `json:"managed_environment_id"`
	Identity             Identity `json:"identity"`
}

// Ingress configures external traffic. Omit the whole block for no ingress.
type Ingress struct {
	External       bool            `json:"external"`
	TargetPort     int             `json:"target_port"`
	Transport      string          `json:"transport,omitempty"`       // auto|http|http2|tcp
	AllowInsecure  bool            `json:"allow_insecure,omitempty"`
	Cors           *Cors           `json:"cors,omitempty"`
	IPRestrictions []IPRestriction `json:"ip_restrictions,omitempty"`
	Traffic        *Traffic        `json:"traffic,omitempty"`
}

type Cors struct {
	AllowedOrigins   []string `json:"allowed_origins,omitempty"`
	AllowedMethods   []string `json:"allowed_methods,omitempty"`
	AllowedHeaders   []string `json:"allowed_headers,omitempty"`
	ExposeHeaders    []string `json:"expose_headers,omitempty"`
	AllowCredentials bool     `json:"allow_credentials,omitempty"`
	MaxAge           int      `json:"max_age,omitempty"`
}

type IPRestriction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CIDR        string `json:"cidr"`
	Action      string `json:"action"` // allow|deny
}

// Traffic is a map-shaped blue/green split: at most one latest and one
// previous. Previous is resolved at deploy time to the current
// latestRevisionName of the deployed app.
type Traffic struct {
	Latest   *TrafficEntry `json:"latest,omitempty"`
	Previous *TrafficEntry `json:"previous,omitempty"`
}

type TrafficEntry struct {
	Weight int    `json:"weight"`
	Label  string `json:"label,omitempty"`
}

// Registry is one container-image source. Exactly one of the two auth modes
// applies: managed-identity (Identity field, defaults to App.Identity) or
// basic auth (Username + PasswordSecret).
type Registry struct {
	Server         string   `json:"server"`
	Identity       Identity `json:"identity,omitempty"`
	Username       string   `json:"username,omitempty"`
	PasswordSecret string   `json:"password_secret,omitempty"`
}

// Scale — replica bounds and optional autoscaling rules. Omitting the whole
// block gives {min: 1, max: 1} (Lazure default, not Azure's scale-to-zero).
type Scale struct {
	Min             int         `json:"min,omitempty"`
	Max             int         `json:"max,omitempty"`
	CooldownPeriod  int         `json:"cooldown_period,omitempty"`
	PollingInterval int         `json:"polling_interval,omitempty"`
	Rules           []ScaleRule `json:"rules,omitempty"`
}

// ScaleRule — exactly one of Http/Tcp/CPU/Memory/AzureQueue/Custom is set.
type ScaleRule struct {
	Name       string            `json:"name"`
	Http       *HTTPScaler       `json:"http,omitempty"`
	TCP        *TCPScaler        `json:"tcp,omitempty"`
	CPU        *MetricScaler     `json:"cpu,omitempty"`
	Memory     *MetricScaler     `json:"memory,omitempty"`
	AzureQueue *AzureQueueScaler `json:"azure_queue,omitempty"`
	Custom     *CustomScaler     `json:"custom,omitempty"`
	Auth       *RuleAuth         `json:"auth,omitempty"`
}

type HTTPScaler struct {
	ConcurrentRequests int `json:"concurrent_requests"`
}

type TCPScaler struct {
	ConcurrentConnections int `json:"concurrent_connections"`
}

type MetricScaler struct {
	Type  string  `json:"type"` // utilization | average_value
	Value float64 `json:"value"`
}

type AzureQueueScaler struct {
	QueueName   string `json:"queue_name"`
	QueueLength int    `json:"queue_length"`
	AccountName string `json:"account_name"`
}

type CustomScaler struct {
	Type     string            `json:"type"` // KEDA scaler name, not validated
	Metadata map[string]string `json:"metadata,omitempty"`
}

// RuleAuth.Identity accepts the literal "app" (resolve to App.Identity at
// transform time) or a full resource id. Empty with an auth-needing rule
// means default to "app".
type RuleAuth struct {
	Identity string           `json:"identity,omitempty"`
	Secrets  []RuleSecretAuth `json:"secrets,omitempty"`
}

type RuleSecretAuth struct {
	Param  string `json:"param"`
	Secret string `json:"secret"`
}

// Volume — an app-wide volume definition. azure_file and nfs_azure_file
// require storage_name referencing a pre-registered managedEnvironmentStorage.
type Volume struct {
	Name         string `json:"name"`
	Type         string `json:"type"` // empty_dir|azure_file|nfs_azure_file|secret
	StorageName  string `json:"storage_name,omitempty"`
	MountOptions string `json:"mount_options,omitempty"`
}

// Container models both runtime containers and init containers. Init
// containers must not set Probes; validate.go rejects probes on init.
type Container struct {
	Name         string               `json:"name"`
	Image        string               `json:"image,omitempty"`
	Command      []string             `json:"command,omitempty"`
	Args         []string             `json:"args,omitempty"`
	WorkingDir   string               `json:"working_dir,omitempty"`
	Resources    *Resources           `json:"resources,omitempty"`
	Env          map[string]*EnvValue `json:"env,omitempty"`       // replaces shared
	MergeEnv     map[string]*EnvValue `json:"merge_env,omitempty"` // inherits + overrides shared
	VolumeMounts []VolumeMount        `json:"volume_mounts,omitempty"`
	Probes       *Probes              `json:"probes,omitempty"`
}

type Resources struct {
	CPU    float64 `json:"cpu"`
	Memory string  `json:"memory"`
}

type VolumeMount struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	SubPath string `json:"sub_path,omitempty"`
}

type Probes struct {
	Liveness  *Probe `json:"liveness,omitempty"`
	Readiness *Probe `json:"readiness,omitempty"`
	Startup   *Probe `json:"startup,omitempty"`
}

// Probe — exactly one of Http/TCP/Exec is set per entry.
type Probe struct {
	HTTP             *HTTPProbe `json:"http,omitempty"`
	TCP              *TCPProbe  `json:"tcp,omitempty"`
	Exec             []string   `json:"exec,omitempty"`
	InitialDelay     int        `json:"initial_delay,omitempty"`
	Period           int        `json:"period,omitempty"`
	Timeout          int        `json:"timeout,omitempty"`
	FailureThreshold int        `json:"failure_threshold,omitempty"`
	SuccessThreshold int        `json:"success_threshold,omitempty"`
}

type HTTPProbe struct {
	Path   string `json:"path"`
	Port   int    `json:"port"`
	Scheme string `json:"scheme,omitempty"` // http|https (default http)
}

type TCPProbe struct {
	Port int `json:"port"`
}
