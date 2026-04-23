package azurearm

import "time"

// Revision models a single ContainerApp revision as returned by the
// ARM revisions endpoint. Nearly everything here is read-only —
// revisions are created as a side effect of PUTs, not directly.
type Revision struct {
	Name       string             `json:"name"`
	Properties RevisionProperties `json:"properties"`
}

// RevisionProperties holds the runtime state of a revision. All fields
// are read-only.
type RevisionProperties struct {
	CreatedTime       time.Time `json:"createdTime,omitempty"`
	LastActiveTime    time.Time `json:"lastActiveTime,omitempty"`
	Active            bool      `json:"active,omitempty"`
	Replicas          int       `json:"replicas,omitempty"`
	TrafficWeight     int       `json:"trafficWeight,omitempty"`
	ProvisioningState string    `json:"provisioningState,omitempty"`
	HealthState       string    `json:"healthState,omitempty"`
	RunningState      string    `json:"runningState,omitempty"`
	FQDN              string    `json:"fqdn,omitempty"`
}

// Replica models one running instance of a revision.
type Replica struct {
	Name       string            `json:"name"`
	Properties ReplicaProperties `json:"properties"`
}

type ReplicaProperties struct {
	CreatedTime    time.Time          `json:"createdTime,omitempty"`
	RunningState   string             `json:"runningState,omitempty"`
	Containers     []ReplicaContainer `json:"containers,omitempty"`
	InitContainers []ReplicaContainer `json:"initContainers,omitempty"`
}

// ReplicaContainer is the per-container state inside a replica —
// lets us answer "is the tasks container running?" independently of
// the app container.
type ReplicaContainer struct {
	Name         string `json:"name"`
	ContainerID  string `json:"containerId,omitempty"`
	Ready        bool   `json:"ready,omitempty"`
	Started      bool   `json:"started,omitempty"`
	RestartCount int    `json:"restartCount,omitempty"`
	RunningState string `json:"runningState,omitempty"`
}
