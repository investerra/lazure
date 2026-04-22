package lazurecfg

// Identity is a full UserAssigned managed-identity resource id of the form
//
//	/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.ManagedIdentity/userAssignedIdentities/{name}
//
// Declared as a distinct type so custom YAML decoding (accepting either a
// bare string or, in the future, an object shape for multi-identity / system
// assigned support) can be layered on in task lazure-697.4. Also in that
// task: a SubscriptionID() parser to pull the {sub} segment out for ARM URL
// construction.
type Identity string
