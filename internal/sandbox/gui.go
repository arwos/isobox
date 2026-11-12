package sandbox

const sandboxGUIRuntime = "/run/isobox"

type guiSetup struct {
	mounts      []profileMount
	environment map[string]string
	runtimeDir  bool
}
