package web

// Resources (RES-01): the Overview card that shows each container's memory limit and the website's PHP settings,
// on our standard until the owner raises them from a short list (never below the standard).

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

type ramRow struct {
	Service  string
	Standard int
	Current  int
	PHP      bool
	Choices  []int // the standard first, then the raises
}

type phpRow struct {
	Key      string // form field and locale key
	Unit     string // MB or s
	Standard int
	Current  int
	Choices  []int
}

type limitsView struct {
	RAM     []ramRow
	PHP     []phpRow
	HostMB  int
	TotalMB int
	Over    bool // all websites' limits together are more than the server's memory
}

// mb shows a size in MB the way people read it.
func mb(n int) string {
	if n >= 1024 && n%512 == 0 {
		return strconv.FormatFloat(float64(n)/1024, 'f', -1, 64) + " GB"
	}
	return fmt.Sprintf("%d MB", n)
}

func limitsOf(l agent.Limits) *limitsView {
	v := &limitsView{HostMB: l.HostMB, TotalMB: l.TotalMB, Over: l.HostMB > 0 && l.TotalMB > l.HostMB}
	for _, s := range l.Services {
		if s.Service == "cli" {
			continue
		}
		r := ramRow{Service: s.Service, Standard: s.Standard, Current: s.Current, PHP: s.PHP, Choices: []int{s.Standard}}
		for _, c := range l.RAMChoice {
			if c > s.Standard {
				r.Choices = append(r.Choices, c)
			}
		}
		v.RAM = append(v.RAM, r)
	}
	if p := l.PHP; p != nil {
		v.PHP = []phpRow{
			{Key: "memory_mb", Unit: "MB", Standard: l.PHPStd.Memory, Current: p.Memory, Choices: l.PHPChoice["memory_mb"]},
			{Key: "upload_mb", Unit: "MB", Standard: l.PHPStd.Upload, Current: p.Upload, Choices: l.PHPChoice["upload_mb"]},
			{Key: "exec_seconds", Unit: "s", Standard: l.PHPStd.ExecTime, Current: p.ExecTime, Choices: l.PHPChoice["exec_seconds"]},
			{Key: "max_input_vars", Unit: "", Standard: l.PHPStd.InputVars, Current: p.InputVars, Choices: l.PHPChoice["max_input_vars"]},
		}
	}
	return v
}

// limitChange reads the Resources form: one select per container and, when shown, one per PHP setting.
func limitChange(form url.Values, l agent.Limits) (agent.LimitChange, error) {
	var c agent.LimitChange
	for _, s := range l.Services {
		v := form.Get("ram." + s.Service)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("memory of %s is not a number", s.Service)
		}
		if c.RAM == nil {
			c.RAM = map[string]int{}
		}
		c.RAM[s.Service] = n
	}
	if l.PHP != nil && form.Get("php.memory_mb") != "" {
		vals := map[string]int{}
		for _, k := range []string{"memory_mb", "upload_mb", "exec_seconds", "max_input_vars"} {
			n, err := strconv.Atoi(form.Get("php." + k))
			if err != nil {
				return c, fmt.Errorf("%s is not a number", k)
			}
			vals[k] = n
		}
		c.PHP = &agent.PHPSettings{Memory: vals["memory_mb"], Upload: vals["upload_mb"], ExecTime: vals["exec_seconds"], InputVars: vals["max_input_vars"]}
	}
	return c, nil
}

// limitNote names the change for the activity log.
func limitNote(c agent.LimitChange) string {
	var p []string
	for k, v := range c.RAM {
		p = append(p, fmt.Sprintf("%s %d MB", k, v))
	}
	sort.Strings(p)
	if c.PHP != nil {
		p = append(p, fmt.Sprintf("PHP memory %d MB, upload %d MB, time %d s, input vars %d", c.PHP.Memory, c.PHP.Upload, c.PHP.ExecTime, c.PHP.InputVars))
	}
	return strings.Join(p, ", ")
}
