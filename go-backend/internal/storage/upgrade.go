package storage

import (
	"os"
	"path/filepath"
	"strings"
)

// UpgradeFileStorage ports SnappyMail\Upgrade::FileStorage(): move the storage
// files of the old, unsecured layout below data/cfg/ into the per account
// folders and drop the folders it replaces.
//
// The files of an old account folder are renamed into
// <data>/<domain>/<localpart>/; an old "sign_me" folder is deleted instead of
// moved, because the layout stored the credentials in plain text
// (https://github.com/RainLoop/rainloop-webmail/issues/2133).
func UpgradeFileStorage(dataPath string) {
	dataPath = strings.TrimRight(strings.TrimSpace(dataPath), `\/`)
	if "" == dataPath {
		return
	}

	// /cfg/<shard>/<email>
	for _, oldDir := range subDirectories(filepath.Join(dataPath, "cfg")) {
		for _, domainDir := range subDirectories(oldDir) {
			localPart, domain := splitEmail(filepath.Base(domainDir))
			if "" == domain {
				domain = "unknown.tld"
			}
			if "" == localPart {
				localPart = ".unknown"
			}

			newDir := filepath.Join(dataPath, SecureFileName(domain), SecureFileName(localPart))
			if err := Mkdir(newDir); nil != err {
				continue
			}

			entries, err := os.ReadDir(domainDir)
			if nil != err {
				continue
			}
			for _, entry := range entries {
				name := entry.Name()
				source := filepath.Join(domainDir, name)
				if "sign_me" == name {
					_ = os.Remove(source)
					continue
				}
				_ = os.Rename(source, filepath.Join(newDir, name))
			}

			RecRmDir(domainDir)
		}
	}

	RecRmDir(filepath.Join(dataPath, "cfg"))
	RecRmDir(filepath.Join(dataPath, "data"))
	RecRmDir(filepath.Join(dataPath, "files"))
}
