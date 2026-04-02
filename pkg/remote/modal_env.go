package remote

import "os"

// cliModalEnv is set when gotoni is run with --modal-env (root PersistentPreRun).
var cliModalEnv string

// SetModalEnvironmentFromCLI is called by cmd when --modal-env is passed.
func SetModalEnvironmentFromCLI(e string) { cliModalEnv = e }

// ModalEnvironment returns the Modal workspace environment name used by gotoni (Sandboxes, FromName, etc.).
//
// Priority:
//  1. --modal-env (CLI flag for this process)
//  2. GOTONI_MODAL_ENV — gotoni-specific override in the shell
//  3. MODAL_ENVIRONMENT — same as Modal CLI / Python SDK / ~/.modal.toml
//
// Empty string means "use Modal SDK defaults" (active profile in ~/.modal.toml).
func ModalEnvironment() string {
	if cliModalEnv != "" {
		return cliModalEnv
	}
	if e := os.Getenv("GOTONI_MODAL_ENV"); e != "" {
		return e
	}
	return os.Getenv("MODAL_ENVIRONMENT")
}

// ModalEnvironmentOrDefault returns ModalEnvironment() when set; otherwise "alessio-dev".
// Use this for Modal API calls that must be scoped to a single workspace: Sandboxes.List with an
// empty Environment can return sandboxes across environments; an explicit name fixes that.
// Override via --modal-env, GOTONI_MODAL_ENV, or MODAL_ENVIRONMENT (e.g. main, prod).
func ModalEnvironmentOrDefault() string {
	if e := ModalEnvironment(); e != "" {
		return e
	}
	return "alessio-dev"
}
