package scanner

// Test-only exports of plan facts internals for the external scanner_test package.

var ComputePlanFSFingerprint = computePlanFSFingerprint
var CollectPlanDBFacts = collectPlanDBFacts
var ScopeLibraries = scopeLibraries
var CurrentPlanConfigSnapshot = currentPlanConfigSnapshot
var CheckConfigStaleness = checkConfigStaleness
var CheckDBStaleness = checkDBStaleness
var CheckFSStaleness = checkFSStaleness
var PlanExpiry = planExpiry
var PlanIsExpired = planIsExpired
var HashJSON = hashJSON
var StableJSON = stableJSON
var DecodePlanTargets = decodePlanTargets

// FsFingerprintOfFolder exposes the stat-only folder fingerprint used by the plan facts.
func FsFingerprintOfFolder(libID int, f *folderEntry) string {
	h := &fsFingerprintHasher{}
	h.addFolder(libID, f)
	return h.sum()
}
