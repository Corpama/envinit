package runner

import (
	"fmt"
	"strings"
)

func (a *App) missingRequestedPackages(packages []string) ([]string, error) {
	if a.DryRun {
		return packages, nil
	}
	var missing []string
	for _, request := range packages {
		if a.usesYum() {
			if _, err := a.captureCmd("", nil, "rpm", "-q", request); err != nil {
				missing = append(missing, request)
			}
			continue
		}
		name, want, _ := strings.Cut(request, "=")
		out, err := a.captureCmd("", nil, "dpkg-query", "-W", "-f=${Status}\t${Version}", name)
		if err != nil {
			missing = append(missing, request)
			continue
		}
		status, version, _ := strings.Cut(strings.TrimSpace(out), "\t")
		if status != "install ok installed" {
			return nil, fmt.Errorf("package %s is in partial state %q; repair package manager state before retry", name, status)
		}
		if want != "" && want != version {
			missing = append(missing, request)
		}
	}
	return missing, nil
}

func (a *App) missingLocalPackages(packages []string) ([]string, error) {
	if a.DryRun {
		return packages, nil
	}
	var missing []string
	for _, path := range packages {
		if a.usesYum() {
			meta, err := a.captureCmd("", nil, "rpm", "-qp", "--qf", "%{NAME}\t%{EPOCHNUM}:%{VERSION}-%{RELEASE}\t%{ARCH}", path)
			if err != nil {
				return nil, err
			}
			fields := strings.Split(strings.TrimSpace(meta), "\t")
			if len(fields) != 3 {
				return nil, fmt.Errorf("invalid RPM metadata: %s", path)
			}
			installed, err := a.captureCmd("", nil, "rpm", "-q", "--qf", "%{NAME}\t%{EPOCHNUM}:%{VERSION}-%{RELEASE}\t%{ARCH}", fields[0]+"."+fields[2])
			if err != nil || strings.TrimSpace(installed) != strings.TrimSpace(meta) {
				missing = append(missing, path)
			}
		} else {
			meta, err := a.captureCmd("", nil, "dpkg-deb", "-f", path, "Package", "Version", "Architecture")
			if err != nil {
				return nil, err
			}
			fields := map[string]string{}
			for _, line := range strings.Split(meta, "\n") {
				k, v, ok := strings.Cut(line, ":")
				if ok {
					fields[k] = strings.TrimSpace(v)
				}
			}
			if fields["Package"] == "" || fields["Version"] == "" || fields["Architecture"] == "" {
				return nil, fmt.Errorf("invalid DEB metadata: %s", path)
			}
			name := fields["Package"]
			if fields["Architecture"] != "all" {
				name += ":" + fields["Architecture"]
			}
			pending, err := a.missingRequestedPackages([]string{name + "=" + fields["Version"]})
			if err != nil {
				return nil, err
			}
			if len(pending) > 0 {
				missing = append(missing, path)
			}
		}
	}
	return missing, nil
}
