package agentbinary

import (
	"debug/buildinfo"
	"debug/elf"
	"fmt"
)

// Validate inspects releases without executing an uploaded program on the server.
func Validate(path, goos, arch string) error {
	if goos != "linux" {
		return fmt.Errorf("Agent releases must target linux")
	}
	machine, ok := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[arch]
	if !ok {
		return fmt.Errorf("unsupported Agent architecture %q", arch)
	}
	binary, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("invalid Agent ELF executable: %w", err)
	}
	defer binary.Close()
	if binary.Machine != machine || binary.Class != elf.ELFCLASS64 || binary.Type != elf.ET_EXEC {
		return fmt.Errorf("Agent executable does not match %s/%s", goos, arch)
	}
	for _, program := range binary.Progs {
		if program.Type == elf.PT_INTERP {
			return fmt.Errorf("Agent executable must be statically linked")
		}
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil || info.Path != "baize/agent/cmd/baize-agent" {
		return fmt.Errorf("executable is not a Baize Agent build")
	}
	settings := make(map[string]string)
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"] != goos || settings["GOARCH"] != arch {
		return fmt.Errorf("Agent build platform does not match release metadata")
	}
	return nil
}
