// fs package wraps btrfs utilities to manage filesystem snapshots, quotas, access mode, etc.
package fs

import (
	"bufio"
	"bytes"
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/subutai-io/agent/config"
	"github.com/subutai-io/agent/log"
)

// IsSubvolumeReadonly checks if BTRFS subvolume have "readonly" property.
// It's used in Subutai to check if LXC container template or not.
func IsSubvolumeReadonly(path string) bool {
	out, err := exec.Command("btrfs", "property", "get", "-ts", path).Output()
	log.Check(log.DebugLevel, "Getting BTRFS subvolume readonly property", err)
	return strings.Contains(string(out), "true")
}

// IsSubvolume checks if path BTRFS subvolume.
func IsSubvolume(path string) bool {
	out, err := exec.Command("btrfs", "subvolume", "show", path).CombinedOutput()
	log.Check(log.DebugLevel, "Checking is path BTRFS subvolume", err)
	return strings.Contains(string(out), "Subvolume ID")
}

// SubvolumeCreate creates BTRFS subvolume.
func SubvolumeCreate(dst string) {
	if id(dst) == "" {
		out, err := exec.Command("btrfs", "subvolume", "create", dst).CombinedOutput()
		log.Check(log.FatalLevel, "Creating subvolume "+dst+": "+string(out), err)
	}
}

// SubvolumeClone creates snapshot of the BTRFS subvolume.
func SubvolumeClone(src, dst string) {
	out, err := exec.Command("btrfs", "subvolume", "snapshot", src, dst).CombinedOutput()
	log.Check(log.FatalLevel, "Creating snapshot: "+string(out), err)
}

// SubvolumeDestroy deletes BTRFS subvolume and all subdirectories.
// It also destroys quota groups.
func SubvolumeDestroy(path string) {
	svol := []string{path}
	if !IsSubvolume(path) {
		svol = []string{path + "/var", path + "/opt", path + "/home", path + "/rootfs"}
	}
	for _, v := range svol {
		nestedvol, err := exec.Command("btrfs", "subvolume", "list", "-o", v).Output()
		log.Check(log.DebugLevel, "Getting nested subvolumes in "+v, err)
		scanner := bufio.NewScanner(bytes.NewReader(nestedvol))
		for scanner.Scan() {
			line := strings.Fields(scanner.Text())
			if len(line) > 8 {
				SubvolumeDestroy(GetBtrfsRoot() + line[8])
			}
		}
		qgroupDestroy(v)
		out, err := exec.Command("btrfs", "subvolume", "delete", v).CombinedOutput()
		log.Check(log.DebugLevel, "Destroying subvolume "+v+": "+string(out), err)
	}
	log.Check(log.DebugLevel, "Removing path "+path, exec.Command("rm", "-rf", path).Run())
}

// qgroupDestroy delete quota group for BTRFS subvolume.
func qgroupDestroy(path string) {
	index := id(path)
	out, err := exec.Command("btrfs", "qgroup", "destroy", index, config.Agent.LxcPrefix).CombinedOutput()
	log.Check(log.DebugLevel, "Destroying qgroup "+path+" "+index+": "+string(out), err)
}

// NEED REFACTORING
func id(path string) string {
	path = strings.Replace(path, config.Agent.LxcPrefix, "", -1)
	out, err := exec.Command("btrfs", "subvolume", "list", config.Agent.LxcPrefix).Output()
	log.Check(log.DebugLevel, "Getting BTRFS subvolume list", err)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.Fields(scanner.Text())
		if len(line) > 8 {
			if strings.HasSuffix(line[8], path) {
				return line[1]
			}
		}
	}
	return ""
}

// Receive creates BTRFS subvolume using saved delta-file, it can depend on some parent.
// Parent subvolume should be installed before receiving child subvolume.
func Receive(src, dst, delta string, parent bool) {
	args := []string{"receive", "-p", src, dst}
	if !parent {
		args = []string{"receive", dst}
	}
	log.Debug(strings.Join(args, " "))
	receive := exec.Command("btrfs", args...)
	input, err := os.Open(config.Agent.LxcPrefix + "tmpdir/" + delta)
	if !log.Check(log.FatalLevel, "Opening delta "+delta, err) {
		defer input.Close()
		receive.Stdin = input
		out, err := receive.CombinedOutput()
		log.Check(log.FatalLevel, "Receiving delta "+delta+": "+string(out), err)
	}
}

// Send creates delta-file using BTRFS subvolume, it can depend on some parent.
func Send(src, dst, delta string) error {
	tmpDir, err := ioutil.TempDir(config.Agent.LxcPrefix+"tmpdir/", "export")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	if path := strings.Split(dst, "/"); len(path) > 0 {
		tmpVolume := tmpDir + "/" + path[len(path)-1]

		SubvolumeClone(dst, tmpVolume)
		defer SubvolumeDestroy(tmpVolume)
		SetVolReadOnly(tmpVolume, true)

		if src != dst {
			err = exec.Command("btrfs", "send", "-p", src, tmpVolume, "-f", delta).Run()
		} else {
			err = exec.Command("btrfs", "send", tmpVolume, "-f", delta).Run()
		}
		return err
	}
	return nil
}

// ReadOnly sets readonly flag for Subutai container.
// Subvolumes with active readonly flag is Subutai templates.
func ReadOnly(container string, flag bool) {
	for _, path := range []string{container + "/rootfs/", container + "/opt", container + "/var", container + "/home"} {
		SetVolReadOnly(config.Agent.LxcPrefix+path, flag)
	}
}

// SetVolReadOnly sets readonly flag for BTRFS subvolume.
func SetVolReadOnly(subvol string, flag bool) {
	arg := []string{"property", "set", "-ts", subvol, "ro", strconv.FormatBool(flag)}
	out, err := exec.Command("btrfs", arg...).CombinedOutput()
	log.Check(log.FatalLevel, "Setting readonly: "+strconv.FormatBool(flag)+": "+string(out), err)
}

// Stat returns quota and usage for BTRFS subvolume.
func Stat(path, index string, raw bool) (value string) {
	var row = map[string]int{"quota": 3, "usage": 2}

	args := []string{"qgroup", "show", "-r", config.Agent.LxcPrefix}
	if raw {
		args = []string{"qgroup", "show", "-r", "--raw", config.Agent.LxcPrefix}
	}
	out, err := exec.Command("btrfs", args...).Output()
	log.Check(log.FatalLevel, "Getting btrfs stats", err)
	ind := id(path)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.Fields(scanner.Text())
		if len(line) > 3 {
			if strings.HasSuffix(line[0], "/"+ind) {
				value = line[row[index]]
			}
		}
	}
	return value
}

// DiskQuota returns total disk quota for Subutai container.
// If size argument is set, it sets new quota value.
func DiskQuota(path string, size ...string) string {
	parent := id(path)
	exec.Command("btrfs", "qgroup", "create", "1/"+parent, config.Agent.LxcPrefix+path).Run()
	exec.Command("btrfs", "qgroup", "assign", "0/"+id(path+"/opt"), "1/"+parent, config.Agent.LxcPrefix+path).Run()
	exec.Command("btrfs", "qgroup", "assign", "0/"+id(path+"/var"), "1/"+parent, config.Agent.LxcPrefix+path).Run()
	exec.Command("btrfs", "qgroup", "assign", "0/"+id(path+"/home"), "1/"+parent, config.Agent.LxcPrefix+path).Run()
	exec.Command("btrfs", "qgroup", "assign", "0/"+id(path+"/rootfs"), "1/"+parent, config.Agent.LxcPrefix+path).Run()

	if size != nil {
		if out, err := exec.Command("btrfs", "qgroup", "limit", "-e", size[0]+"G", "1/"+parent, config.Agent.LxcPrefix+path).CombinedOutput(); err != nil {
			return err.Error() + string(out)
		}
	}
	return Stat(path, "quota", false)
}

// Quota returns subvolume quota.
// If size argument is set, it sets new quota value.
func Quota(path string, size ...string) string {
	if len(size) > 0 && len(size[0]) > 0 {
		if err := exec.Command("btrfs", "qgroup", "limit", size[0]+"G", config.Agent.LxcPrefix+path).Run(); err != nil {
			return err.Error()
		}
	}
	return Stat(path, "quota", false)
}

// GetBtrfsRoot returns BTRFS root
func GetBtrfsRoot() string {
	data, err := exec.Command("findmnt", "-nT", config.Agent.LxcPrefix).Output()
	log.Check(log.FatalLevel, "Searching btrfs mount point", err)

	line := strings.Fields(string(data))
	return line[0] + "/"
}
