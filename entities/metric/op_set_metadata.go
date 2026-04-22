package metric

// SetMetadataOp is kept as a generic dag.SetMetadataOperation[*Snapshot]
// — no series-specific behaviour needed on top of the generic one.
// This file exists mainly as a place to document the decision; the
// unmarshaler in operation.go handles it directly.
