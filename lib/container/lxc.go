//Package container main function is to provide control interface for Subutai containers through go-lxc bindings and system-level libraries and executables
package container

import (
	"bufio"
	"bytes"
	"errors"
	"io/ioutil"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/subutai-io/agent/config"
	"github.com/subutai-io/agent/db"
	"github.com/subutai-io/agent/lib/fs"
	"github.com/subutai-io/agent/lib/net"
	"github.com/subutai-io/agent/log"

	"gopkg.in/lxc/go-lxc.v2"
)

// All returns list of all containers
func All() []string {
	return lxc.DefinedContainerNames(config.Agent.LxcPrefix)
}

// IsTemplate checks if Subutai container is template.
func IsTemplate(name string) bool {
	return fs.IsSubvolumeReadonly(config.Agent.LxcPrefix + name + "/rootfs/")
}

// Templates returns list of all templates
func Templates() (containers []string) {
	for _, name := range All() {
		if IsTemplate(name) {
			containers = append(containers, name)
		}
	}
	return
}

// Containers returns list of all containers
func Containers() (containers []string) {
	for _, name := range All() {
		if !IsTemplate(name) {
			containers = append(containers, name)
		}
	}
	return
}

// IsContainer checks is container exist.
func IsContainer(name string) bool {
	for _, item := range All() {
		if name == item {
			return true
		}
	}
	return false
}

// State returns container stat in human readable format.
func State(name string) (state string) {
	c, err := lxc.NewContainer(name, config.Agent.LxcPrefix)
	if err != nil {
		return "UNKNOWN"
	}
	switch c.State() {
	case lxc.STOPPED:
		return "STOPPED"
	case lxc.RUNNING:
		return "RUNNING"
	case lxc.STARTING:
		return "STARTING"
	case lxc.STOPPING:
		return "STOPPING"
	case lxc.ABORTING:
		return "ABORTING"
	case lxc.FREEZING:
		return "FREEZING"
	case lxc.FROZEN:
		return "FROZEN"
	case lxc.THAWED:
		return "THAWED"
	}
	return "UNKNOWN"
}

// SetApt configures APT configuration inside Subutai container.
func SetApt(name string) {
	root := GetParent(name)
	for parent := name; root != parent; root = GetParent(parent) {
		parent = root
	}
	if root != "master" {
		return
	}
	gateway := GetConfigItem(config.Agent.LxcPrefix+name+"/config", "lxc.network.ipv4.gateway")
	if len(gateway) == 0 {
		gateway = "10.10.10.254"
	}

	repo := []byte("deb http://" + gateway + "/apt/main trusty main restricted universe multiverse\n" +
		"deb http://" + gateway + "/apt/main trusty-updates main restricted universe multiverse\n" +
		"deb http://" + gateway + "/apt/security trusty-security main restricted universe multiverse\n")
	log.Check(log.DebugLevel, "Writing apt source repo list",
		ioutil.WriteFile(config.Agent.LxcPrefix+name+"/rootfs/etc/apt/sources.list", repo, 0644))

	// kurjun := []byte("deb [arch=amd64,all] http://" + config.Management.Host + ":8330/rest/kurjun/vapt trusty main contrib\n" +
	// 	"deb [arch=amd64,all] http://" + config.Cdn.Url + ":8330/kurjun/rest/deb trusty main contrib\n")
	kurjun := []byte("deb http://" + config.CDN.URL + ":8080/kurjun/rest/apt /\n")
	log.Check(log.DebugLevel, "Writing apt source kurjun list",
		ioutil.WriteFile(config.Agent.LxcPrefix+name+"/rootfs/etc/apt/sources.list.d/subutai-repo.list", kurjun, 0644))
}

// Start starts the Subutai container.
func Start(name string) {
	c, err := lxc.NewContainer(name, config.Agent.LxcPrefix)
	log.Check(log.FatalLevel, "Looking for container "+name, err)
	log.Check(log.DebugLevel, "Starting LXC container", c.Start())

	if _, err := os.Stat(config.Agent.LxcPrefix + name + "/.stop"); err == nil {
		log.Check(log.WarnLevel, "Deleting .stop file to "+name, os.Remove(config.Agent.LxcPrefix+name+"/.stop"))
	}
	if _, err := os.Stat(config.Agent.LxcPrefix + name + "/.start"); os.IsNotExist(err) {
		f, err := os.Create(config.Agent.LxcPrefix + name + "/.start")
		log.Check(log.WarnLevel, "Creating .start file to "+name, err)
		log.Check(log.WarnLevel, "Closing .start file "+name, f.Close())
	}
}

// Stop stops the Subutai container.
func Stop(name string) {
	c, err := lxc.NewContainer(name, config.Agent.LxcPrefix)
	log.Check(log.FatalLevel, "Looking for container "+name, err)
	log.Check(log.DebugLevel, "Stopping LXC container", c.Stop())

	if _, err := os.Stat(config.Agent.LxcPrefix + name + "/.start"); err == nil {
		log.Check(log.WarnLevel, "Creating .start file to "+name, os.Remove(config.Agent.LxcPrefix+name+"/.start"))
	}
	if _, err := os.Stat(config.Agent.LxcPrefix + name + "/.stop"); os.IsNotExist(err) {
		f, err := os.Create(config.Agent.LxcPrefix + name + "/.stop")
		log.Check(log.WarnLevel, "Creating .stop file to "+name, err)
		log.Check(log.WarnLevel, "Closing .stop file "+name, f.Close())
	}
}

// AttachExec executes a command inside Subutai container.
func AttachExec(name string, command []string, env ...[]string) (output []string, err error) {
	if !IsContainer(name) {
		return output, errors.New("Container does not exists")
	}

	container, err := lxc.NewContainer(name, config.Agent.LxcPrefix)
	if container.State() != lxc.RUNNING || err != nil {
		return output, errors.New("Container is " + container.State().String())
	}

	bufR, bufW, err := os.Pipe()
	if err != nil {
		return output, errors.New("Failed to create OS pipe")
	}
	bufRErr, bufWErr, err := os.Pipe()
	if err != nil {
		return output, errors.New("Failed to create OS pipe")
	}

	options := lxc.AttachOptions{
		Namespaces: -1,
		UID:        0,
		GID:        0,
		StdoutFd:   bufW.Fd(),
		StderrFd:   bufWErr.Fd(),
	}
	if len(env) > 0 {
		options.Env = env[0]
	}

	_, err = container.RunCommand(command, options)
	log.Check(log.DebugLevel, "Executing command inside container", err)

	log.Check(log.DebugLevel, "Closing write buffer for stdout", bufW.Close())
	defer bufR.Close()

	log.Check(log.DebugLevel, "Closing write buffer for stderr", bufWErr.Close())
	defer bufRErr.Close()

	out := bufio.NewScanner(bufR)
	for out.Scan() {
		output = append(output, out.Text())
	}

	return output, nil
}

// Destroy deletes the Subutai container.
func Destroy(name string) {
	c, err := lxc.NewContainer(name, config.Agent.LxcPrefix)
	if !log.Check(log.WarnLevel, "Creating container object", err) && c.State() == lxc.RUNNING {
		log.Check(log.FatalLevel, "Stopping container", c.Stop())
	}
	fs.SubvolumeDestroy(config.Agent.LxcPrefix + name)

	db, err := db.New()
	log.Check(log.WarnLevel, "Opening database", err)
	log.Check(log.WarnLevel, "Deleting container metadata entry", db.ContainerDel(name))
	log.Check(log.WarnLevel, "Deleting uuid entry", db.DelUuidEntry(name))
	log.Check(log.WarnLevel, "Closing database", db.Close())
}

// GetParent return a parent of the Subutai container.
func GetParent(name string) string {
	if !IsContainer(name) {
		return "Container does not exists"
	}
	configFileName := config.Agent.LxcPrefix + name + "/config"
	return GetConfigItem(configFileName, "subutai.parent")
}

// Clone create the duplicate container from the Subutai template.
func Clone(parent, child string) {
	var backend lxc.BackendStore
	log.Check(log.DebugLevel, "Setting LXC backend to BTRFS", backend.Set("btrfs"))

	c, err := lxc.NewContainer(parent, config.Agent.LxcPrefix)
	log.Check(log.FatalLevel, "Looking for container "+parent, err)

	fs.SubvolumeCreate(config.Agent.LxcPrefix + child)
	err = c.Clone(child, lxc.CloneOptions{Backend: backend})
	log.Check(log.FatalLevel, "Cloning container", err)

	fs.SubvolumeClone(config.Agent.LxcPrefix+parent+"/home", config.Agent.LxcPrefix+child+"/home")
	fs.SubvolumeClone(config.Agent.LxcPrefix+parent+"/opt", config.Agent.LxcPrefix+child+"/opt")
	fs.SubvolumeClone(config.Agent.LxcPrefix+parent+"/var", config.Agent.LxcPrefix+child+"/var")

	SetContainerConf(child, [][]string{
		{"lxc.network.link", ""},
		{"lxc.network.veth.pair", strings.Replace(GetConfigItem(config.Agent.LxcPrefix+child+"/config", "lxc.network.hwaddr"), ":", "", -1)},
		{"lxc.network.script.up", config.Agent.AppPrefix + "bin/create_ovs_interface"},
		{"subutai.parent", parent},
		{"lxc.mount.entry", config.Agent.LxcPrefix + child + "/home home none bind,rw 0 0"},
		{"lxc.mount.entry", config.Agent.LxcPrefix + child + "/opt opt none bind,rw 0 0"},
		{"lxc.mount.entry", config.Agent.LxcPrefix + child + "/var var none bind,rw 0 0"},
		{"lxc.network.mtu", "1300"},
	})
}

// ResetNet sets default parameters of the network configuration for container.
// It's used right before converting container into template.
func ResetNet(name string) {
	SetContainerConf(name, [][]string{
		{"lxc.network.type", "veth"},
		{"lxc.network.flags", "up"},
		{"lxc.network.link", "lxcbr0"},
		{"lxc.network.ipv4.gateway", ""},
		{"lxc.network.veth.pair", ""},
		{"lxc.network.script.up", ""},
		{"lxc.network.mtu", ""},
		{"lxc.network.ipv4", ""},
		{"#vlan_id", ""},
	})
}

// QuotaRAM sets the memory quota to the Subutai container.
// If quota size argument is missing, it's just return current value.
func QuotaRAM(name string, size ...string) int {
	c, err := lxc.NewContainer(name, config.Agent.LxcPrefix)
	log.Check(log.DebugLevel, "Looking for container: "+name, err)
	i, err := strconv.Atoi(size[0])
	log.Check(log.DebugLevel, "Parsing quota size", err)
	if i > 0 {
		log.Check(log.DebugLevel, "Setting memory limit", c.SetMemoryLimit(lxc.ByteSize(i*1024*1024)))
		SetContainerConf(name, [][]string{{"lxc.cgroup.memory.limit_in_bytes", size[0] + "M"}})
	}
	limit, err := c.MemoryLimit()
	log.Check(log.DebugLevel, "Getting memory limit of container: "+name, err)
	return int(limit / 1024 / 1024)
}

// QuotaCPU sets container CPU limitation and return current value in percents.
// If passed value < 100, we assume that this value mean percents.
// If passed value > 100, we assume that this value mean MHz.
func QuotaCPU(name string, size ...string) int {
	c, cErr := lxc.NewContainer(name, config.Agent.LxcPrefix)
	log.Check(log.DebugLevel, "Looking for container: "+name, cErr)
	cfsPeriod := 100000
	tmp, cErr := strconv.Atoi(size[0])
	log.Check(log.DebugLevel, "Parsing quota size", cErr)
	quota := float32(tmp)

	if quota > 100 {
		out, err := ioutil.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq")
		freq, err := strconv.Atoi(strings.TrimSpace(string(out)))
		log.Check(log.DebugLevel, "Parsing quota size", err)
		freq = freq / 1000
		if log.Check(log.DebugLevel, "Getting CPU max frequency", err) {
			out, err := ioutil.ReadFile("/proc/cpuinfo")
			scanner := bufio.NewScanner(bytes.NewReader(out))
			for scanner.Scan() && err == nil {
				if strings.HasPrefix(scanner.Text(), "cpu MHz") {
					freq, err = strconv.Atoi(strings.TrimSpace(strings.Split(strings.Split(scanner.Text(), ":")[1], ".")[0]))
					log.Check(log.DebugLevel, "Parsing quota size", err)
					break
				}
			}
		}
		quota = quota * 100 / float32(freq) / float32(runtime.NumCPU())
	}

	if size[0] != "" && State(name) == "RUNNING" {
		value := strconv.Itoa(int(float32(cfsPeriod) * float32(runtime.NumCPU()) * quota / 100))
		log.Check(log.DebugLevel, "Setting cpu.cfs_quota_us", c.SetCgroupItem("cpu.cfs_quota_us", value))

		SetContainerConf(name, [][]string{{"lxc.cgroup.cpu.cfs_quota_us", value}})
	}

	result, err := strconv.Atoi(c.CgroupItem("cpu.cfs_quota_us")[0])
	log.Check(log.DebugLevel, "Parsing quota size", err)
	return result * 100 / cfsPeriod / runtime.NumCPU()
}

// QuotaCPUset sets particular cores that can be used by the Subutai container.
func QuotaCPUset(name string, size ...string) string {
	c, err := lxc.NewContainer(name, config.Agent.LxcPrefix)
	log.Check(log.DebugLevel, "Looking for container: "+name, err)
	if size[0] != "" {
		log.Check(log.DebugLevel, "Setting cpuset.cpus", c.SetCgroupItem("cpuset.cpus", size[0]))
		SetContainerConf(name, [][]string{{"lxc.cgroup.cpuset.cpus", size[0]}})
	}
	return c.CgroupItem("cpuset.cpus")[0]
}

// QuotaNet sets network bandwidth for the Subutai container.
func QuotaNet(name string, size ...string) string {
	c, err := lxc.NewContainer(name, config.Agent.LxcPrefix)
	log.Check(log.DebugLevel, "Looking for container: "+name, err)
	nic := GetConfigItem(c.ConfigFileName(), "lxc.network.veth.pair")
	if size[0] != "" {
		SetContainerConf(name, [][]string{{"subutai.network.ratelimit", size[0]}})
	}
	return net.RateLimit(nic, size[0])
}

// SetContainerConf sets any parameter in the configuration file of the Subutai container.
func SetContainerConf(container string, conf [][]string) {
	confPath := config.Agent.LxcPrefix + container + "/config"
	newconf := ""

	file, err := os.Open(confPath)
	log.Check(log.FatalLevel, "Opening container config "+confPath, err)
	scanner := bufio.NewScanner(bufio.NewReader(file))

	for scanner.Scan() {
		newline := scanner.Text() + "\n"
		for i := 0; i < len(conf); i++ {
			line := strings.Split(scanner.Text(), "=")
			if len(line) > 1 && strings.Trim(line[0], " ") == conf[i][0] {
				if newline = ""; len(conf[i][1]) > 0 {
					newline = conf[i][0] + " = " + conf[i][1] + "\n"
				}
				conf = append(conf[:i], conf[i+1:]...)
				break
			}
		}
		newconf = newconf + newline
	}
	log.Check(log.DebugLevel, "Closing container configuration file", file.Close())

	for i := range conf {
		if conf[i][1] != "" {
			newconf = newconf + conf[i][0] + " = " + conf[i][1] + "\n"
		}
	}

	log.Check(log.FatalLevel, "Writing container config "+confPath, ioutil.WriteFile(confPath, []byte(newconf), 0644))
}

// GetConfigItem return any parameter from the configuration file of the Subutai container.
func GetConfigItem(path, item string) string {
	if config, err := os.Open(path); err == nil {
		defer config.Close()
		scanner := bufio.NewScanner(config)
		for scanner.Scan() {
			line := strings.Split(scanner.Text(), "=")
			if strings.Trim(line[0], " ") == item {
				return strings.Trim(line[1], " ")
			}
		}
	}
	return ""
}

// SetContainerUID sets UID map shifting for the Subutai container.
// It's required option for any unprivileged LXC container.
func SetContainerUID(c string) {
	uid := "65536"
	if bolt, err := db.New(); err == nil {
		uid = bolt.GetUuidEntry(c)
		log.Check(log.WarnLevel, "Closing database", bolt.Close())
	}

	SetContainerConf(c, [][]string{
		{"lxc.include", config.Agent.AppPrefix + "share/lxc/config/ubuntu.common.conf"},
		{"lxc.include", config.Agent.AppPrefix + "share/lxc/config/ubuntu.userns.conf"},
		{"lxc.id_map", "u 0 " + uid + " 65536"},
		{"lxc.id_map", "g 0 " + uid + " 65536"},
	})

	if s, err := os.Stat(config.Agent.LxcPrefix + c + "/rootfs"); err == nil {
		parentuid := strconv.Itoa(int(s.Sys().(*syscall.Stat_t).Uid))

		err = exec.Command("uidmapshift", "-b", config.Agent.LxcPrefix+c+"/rootfs/", parentuid, uid, "65536").Run()
		log.Check(log.DebugLevel, "uidmapshift rootfs", err)
		err = exec.Command("uidmapshift", "-b", config.Agent.LxcPrefix+c+"/home/", parentuid, uid, "65536").Run()
		log.Check(log.DebugLevel, "uidmapshift home", err)
		err = exec.Command("uidmapshift", "-b", config.Agent.LxcPrefix+c+"/opt/", parentuid, uid, "65536").Run()
		log.Check(log.DebugLevel, "uidmapshift opt", err)
		err = exec.Command("uidmapshift", "-b", config.Agent.LxcPrefix+c+"/var/", parentuid, uid, "65536").Run()
		log.Check(log.DebugLevel, "uidmapshift var", err)

		log.Check(log.ErrorLevel, "Setting chmod 755 on lxc home", os.Chmod(config.Agent.LxcPrefix+c, 0755))
	}
}

// SetDNS configures the Subutai containers to use internal DNS-server from the Resource Host.
func SetDNS(name string) {
	dns := GetConfigItem(config.Agent.LxcPrefix+name+"/config", "lxc.network.ipv4.gateway")
	if len(dns) == 0 {
		dns = "10.10.10.254"
	}

	resolv := []byte("domain\tintra.lan\nsearch\tintra.lan\nnameserver\t" + dns + "\n")
	log.Check(log.DebugLevel, "Writing resolv.conf.orig",
		ioutil.WriteFile(config.Agent.LxcPrefix+name+"/rootfs/etc/resolvconf/resolv.conf.d/original", resolv, 0644))
	log.Check(log.DebugLevel, "Writing resolv.conf.tail",
		ioutil.WriteFile(config.Agent.LxcPrefix+name+"/rootfs/etc/resolvconf/resolv.conf.d/tail", resolv, 0644))
	log.Check(log.DebugLevel, "Writing resolv.conf",
		ioutil.WriteFile(config.Agent.LxcPrefix+name+"/rootfs/etc/resolv.conf", resolv, 0644))
}

// SetEnvID is deprecated function and should be removed.
func SetEnvID(name, envID string) {
	err := os.MkdirAll(config.Agent.LxcPrefix+name+"/rootfs/etc/subutai", 755)
	log.Check(log.FatalLevel, "Creating etc/subutai directory", err)

	config, err := os.Create(config.Agent.LxcPrefix + name + "/rootfs/etc/subutai/lxc-config")
	log.Check(log.FatalLevel, "Creating /etc/subutai/lxc-config file", err)
	defer config.Close()

	_, err = config.WriteString("[Subutai-Agent]\n" + envID + "\n")
	log.Check(log.FatalLevel, "Writing environment id to config", err)
	log.Check(log.DebugLevel, "Synced /etc/subutai/lxc-config", config.Sync())
}

// SetStaticNet sets static IP-address for the Subutai container.
func SetStaticNet(name string) {
	data, err := ioutil.ReadFile(config.Agent.LxcPrefix + name + "/rootfs/etc/network/interfaces")
	log.Check(log.WarnLevel, "Opening /etc/network/interfaces", err)

	err = ioutil.WriteFile(config.Agent.LxcPrefix+name+"/rootfs/etc/network/interfaces",
		[]byte(strings.Replace(string(data), "dhcp", "manual", 1)), 0644)
	log.Check(log.WarnLevel, "Setting internal eth0 interface to manual", err)
}

// DisableSSHPwd disabling SSH password access to the Subutai container.
func DisableSSHPwd(name string) {
	input, err := ioutil.ReadFile(config.Agent.LxcPrefix + name + "/rootfs/etc/ssh/sshd_config")
	if log.Check(log.DebugLevel, "Opening sshd config", err) {
		return
	}

	lines := strings.Split(string(input), "\n")

	for i, line := range lines {
		if strings.EqualFold(line, "#PasswordAuthentication yes") {
			lines[i] = "PasswordAuthentication no"
		}
	}
	output := strings.Join(lines, "\n")
	err = ioutil.WriteFile(config.Agent.LxcPrefix+name+"/rootfs/etc/ssh/sshd_config", []byte(output), 0644)
	log.Check(log.WarnLevel, "Writing new sshd config", err)
}
