package azurearm

// ManagedEnvironment is the Lazure-facing subset of a Container Apps
// managed environment — only what's needed to find the Log Analytics
// workspace backing `lazure logs --type system`.
type ManagedEnvironment struct {
	Name       string                       `json:"name"`
	Properties ManagedEnvironmentProperties `json:"properties"`
}

type ManagedEnvironmentProperties struct {
	AppLogsConfiguration AppLogsConfiguration `json:"appLogsConfiguration"`
}

// AppLogsConfiguration mirrors ARM's log destination setting for the
// environment. Destination is "log-analytics", "azure-monitor", or ""
// (none configured) — system logs are only queryable when it's
// "log-analytics" and LogAnalyticsConfiguration.CustomerID is set.
type AppLogsConfiguration struct {
	Destination               string                     `json:"destination,omitempty"`
	LogAnalyticsConfiguration *LogAnalyticsConfiguration `json:"logAnalyticsConfiguration,omitempty"`
}

// LogAnalyticsConfiguration identifies the workspace. CustomerID is the
// workspace GUID the Log Analytics query API addresses (not an ARM
// resource id) — SharedKey is intentionally omitted since queries
// authenticate via AAD bearer token, not the workspace key.
type LogAnalyticsConfiguration struct {
	CustomerID string `json:"customerId,omitempty"`
}
