package source

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const lxssKey = `Software\Microsoft\Windows\CurrentVersion\Lxss`

// WSLHomes lists the home folders inside this user's WSL distributions,
// as Windows paths (\\wsl.localhost\Ubuntu\home\me). The distributions
// come from the registry; looking inside one starts it if it is stopped,
// which is why this only runs when the user turns WSL on.
func WSLHomes() []string {
	k, err := registry.OpenKey(registry.CURRENT_USER, lxssKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer k.Close()
	ids, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil
	}
	var homes []string
	for _, id := range ids {
		d, err := registry.OpenKey(k, id, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		name, _, err := d.GetStringValue("DistributionName")
		d.Close()
		if err != nil || name == "" {
			continue
		}
		homes = append(homes, distroHomes(name)...)
	}
	return homes
}

// distroHomes lists /home/* and /root of a distribution.
func distroHomes(name string) []string {
	for _, share := range []string{`\\wsl.localhost\`, `\\wsl$\`} { // the second: older Windows 10
		base := share + name
		ents, err := os.ReadDir(filepath.Join(base, "home"))
		if err != nil {
			continue
		}
		var out []string
		for _, e := range ents {
			if e.IsDir() {
				out = append(out, filepath.Join(base, "home", e.Name()))
			}
		}
		return append(out, filepath.Join(base, "root"))
	}
	return nil
}
