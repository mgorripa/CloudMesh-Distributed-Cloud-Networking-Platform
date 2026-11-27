package main

import (
	"cloudmesh/internal/netlab"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

func main() {
	action := flag.String("action", "plan", "plan, up, down")
	flag.Parse()
	cmds, err := netlab.Plan()
	if err != nil {
		log.Fatal(err)
	}
	switch *action {
	case "plan":
		for _, c := range cmds {
			fmt.Println(c.String())
		}
		return
	case "up", "down":
		if os.Geteuid() != 0 {
			log.Fatal("run in a disposable Linux VM with sudo")
		}
	default:
		log.Fatal("invalid action")
	}
	if *action == "down" {
		for _, c := range netlab.Cleanup() {
			if err := c.Run(); err != nil {
				log.Print(err)
			}
		}
		return
	}
	for _, tool := range []string{"ip", "iptables", "ip6tables"} {
		if _, err := exec.LookPath(tool); err != nil {
			log.Fatal(err)
		}
	}
	out, err := exec.Command("ip", "netns", "list").Output()
	if err != nil {
		log.Fatal(err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && (f[0] == netlab.A || f[0] == netlab.B) {
			log.Fatal("lab namespace already exists; inspect and remove it before retrying")
		}
	}
	created := 0
	for i, c := range cmds {
		if err := c.Run(); err != nil {
			for _, cl := range netlab.Cleanup()[:created] {
				_ = cl.Run()
			}
			log.Fatal(err)
		}
		if i < 2 {
			created++
		}
	}
	fmt.Println("lab ready; see docs/linux-lab.md")
}
