package container

import (
	"os"
	"os/exec"

	"github.com/subutai-io/agent/config"
	"github.com/subutai-io/agent/log"
)

var (
	contsStatus map[string]int
)

func init() {
	contsStatus = make(map[string]int)
}

// StateRestore checks container state and starting or stopping containers if required.
func StateRestore() {
	for _, cont := range Active(false) {
		var start, stop bool

		switch contsStatus[cont.Name] {
		case 100:
		case 5:
			{
				log.Debug("Failed to START container " + cont.Name + " after 5 attempts")
				contsStatus[cont.Name] = 100
			}
		case -5:
			{
				log.Debug("Failed to STOP container " + cont.Name + " after 5 attempts")
				contsStatus[cont.Name] = 100
			}
		case 10:
			{
				log.Debug(".start and .stop files exist on " + cont.Name + " cont ")
				contsStatus[cont.Name] = 100
			}
		default:
			{
				if _, err := os.Stat(config.Agent.LxcPrefix + cont.Name + "/.start"); err == nil {
					start = true
				}
				if _, err := os.Stat(config.Agent.LxcPrefix + cont.Name + "/.stop"); err == nil {
					stop = true
				}
				if start && stop {
					contsStatus[cont.Name] = 10
					break
				}
				switch {
				case start && cont.Status != "RUNNING":
					{
						err := exec.Command("subutai", "start", cont.Name).Run()
						log.Check(log.DebugLevel, "Trying to start "+cont.Name, err)
						contsStatus[cont.Name]++
					}
				case stop && cont.Status != "STOPPED":
					{
						err := exec.Command("subutai", "stop", cont.Name).Run()
						log.Check(log.DebugLevel, "Trying to stop "+cont.Name, err)
						contsStatus[cont.Name]--
					}
				default:
					contsStatus[cont.Name] = 0
				}
			}
		}
	}
}
