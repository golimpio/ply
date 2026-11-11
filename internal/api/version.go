package api

// Version is the version of ply's API: the endpoints and the live feed together
// (docs/M11-CLIENTS.md, "Versioning"). New endpoints, fields, event types, topics
// and reasons do not change it; a change that breaks a client does.
const Version = 1

// VersionMin is the oldest Version this server still serves: a client built for
// a Version from VersionMin up to Version works with it.
const VersionMin = 1
