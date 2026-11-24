package repo

// DefaultSandboxProfileID is the sandbox a workflow without an explicit choice
// runs in. The built-in generic Docker profile and its Dockerfile are
// read-only fallbacks, not seed rows; operator-persisted rows take priority.
const DefaultSandboxProfileID = GenericDockerSandboxID
