package configure

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// InspectMaterials checks only names and presence, never hashes large payloads.
func (t *Template) InspectMaterials(project string) error {
	paths := map[string]string{
		"offline_repo.material_path": t.Bundle.OfflineRepo.MaterialPath,
		"artifacts.ofed_archive":     t.Bundle.Artifacts.OFEDArchive,
		"artifacts.xre_installer":    t.Bundle.Artifacts.XREInstaller,
		"artifacts.xdr_archive":      t.Bundle.Artifacts.XDRArchive,
		"artifacts.firmware_archive": t.Bundle.Artifacts.FirmwareArchive,
	}
	for i, path := range t.Bundle.Artifacts.ContainerPackages {
		paths[fmt.Sprintf("artifacts.container_packages[%d]", i)] = path
	}
	keys := make([]string, 0, len(paths))
	for key := range paths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		path := paths[key]
		if path == "" {
			continue
		}
		if strings.ContainsRune(path, 0) {
			return fmt.Errorf("%s: invalid material path", key)
		}
		if !filepath.IsAbs(path) {
			clean := filepath.Clean(path)
			if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return fmt.Errorf("%s: material path escapes project", key)
			}
			path = filepath.Join(project, clean)
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Review = append(t.Review, key+": 材料尚未就绪 "+path)
		} else if err != nil {
			return err
		} else {
			t.Info = append(t.Info, key+": 材料路径存在（SHA256 由 downloader 校验）")
		}
	}
	return nil
}
