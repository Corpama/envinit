package bundle

import (
	"os"
	"os/exec"
	"strings"

	"envinit/internal/spec"
)

type platformDetector struct {
	osReleasePath string
	lookPath      func(string) (string, error)
}

func defaultPlatformDetector() platformDetector {
	return platformDetector{
		osReleasePath: "/etc/os-release",
		lookPath:      exec.LookPath,
	}
}

func applyDetectedPlatformDefaults(b *spec.Bundle, detector platformDetector) {
	osFamilyUnset := strings.TrimSpace(b.Platform.OSFamily) == "" || strings.EqualFold(strings.TrimSpace(b.Platform.OSFamily), "auto")
	packageManagerUnset := strings.TrimSpace(b.Platform.PackageManager) == "" || strings.EqualFold(strings.TrimSpace(b.Platform.PackageManager), "auto")
	if strings.EqualFold(strings.TrimSpace(b.Platform.OSFamily), "auto") {
		b.Platform.OSFamily = ""
	}
	if strings.EqualFold(strings.TrimSpace(b.Platform.PackageManager), "auto") {
		b.Platform.PackageManager = ""
	}
	detected := detector.detect()
	if osFamilyUnset && strings.TrimSpace(detected.OSFamily) != "" {
		b.Platform.OSFamily = detected.OSFamily
	}
	if packageManagerUnset && strings.TrimSpace(detected.PackageManager) != "" {
		b.Platform.PackageManager = detected.PackageManager
	}
	b.Platform.NetworkBackend = reconcileDetectedNetworkBackend(detected, b.Platform.NetworkBackend)
}

func (d platformDetector) detect() spec.PlatformConfig {
	if d.lookPath == nil {
		d.lookPath = exec.LookPath
	}
	out := spec.PlatformConfig{}
	values := readOSRelease(d.osReleasePath)
	id := strings.ToLower(strings.TrimSpace(values["ID"]))
	idLike := strings.ToLower(strings.TrimSpace(values["ID_LIKE"]))
	out.OSFamily = detectedOSFamily(id, idLike)
	switch {
	case out.OSFamily == "ubuntu" || out.OSFamily == "debian":
		out.PackageManager = "apt"
	case detectedRedHatFamily(out.OSFamily):
		out.PackageManager = "yum"
	default:
		out.PackageManager = detectPackageManager(d.lookPath)
		out.OSFamily = detectedOSFamilyFromPackageManager(out.PackageManager)
	}
	return out
}

func readOSRelease(path string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(path) == "" {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return out
}

func detectedOSFamily(id string, idLike string) string {
	for _, value := range append([]string{id}, strings.Fields(idLike)...) {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "ubuntu", "debian":
			return strings.ToLower(strings.TrimSpace(value))
		case "rhel", "redhat":
			return "redhat"
		case "kylin", "rocky", "almalinux", "anolis":
			return strings.ToLower(strings.TrimSpace(value))
		}
	}
	return ""
}

func detectPackageManager(lookPath func(string) (string, error)) string {
	if _, err := lookPath("apt-get"); err == nil {
		return "apt"
	}
	if _, err := lookPath("apt"); err == nil {
		return "apt"
	}
	if _, err := lookPath("yum"); err == nil {
		return "yum"
	}
	if _, err := lookPath("dnf"); err == nil {
		return "yum"
	}
	return ""
}

func detectedOSFamilyFromPackageManager(packageManager string) string {
	switch strings.ToLower(strings.TrimSpace(packageManager)) {
	case "apt":
		return "ubuntu"
	case "yum":
		return "redhat"
	default:
		return ""
	}
}

func detectedRedHatFamily(osFamily string) bool {
	switch strings.ToLower(strings.TrimSpace(osFamily)) {
	case "redhat", "rhel", "kylin", "rocky", "almalinux", "anolis":
		return true
	default:
		return false
	}
}

func reconcileDetectedNetworkBackend(detected spec.PlatformConfig, configured string) string {
	backend := strings.ToLower(strings.TrimSpace(configured))
	switch strings.ToLower(strings.TrimSpace(detected.PackageManager)) {
	case "yum":
		if backend == "netplan" {
			return ""
		}
	case "apt":
		switch backend {
		case "auto", "ifcfg", "network-scripts":
			return ""
		}
	}
	return configured
}
