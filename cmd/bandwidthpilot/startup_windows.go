//go:build windows

package main

import (
	"encoding/xml"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

// Start with Windows.
//
// Bandwidth Pilot needs administrator rights, and Windows silently skips
// programs in the Run registry key that ask for elevation. A Task Scheduler
// task with "run with highest privileges" starts it at sign-in without a
// UAC prompt, which is how other admin tools do it.

const taskName = "Bandwidth Pilot"

func schtasks(args ...string) ([]byte, error) {
	cmd := exec.Command("schtasks.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd.CombinedOutput()
}

func startupEnabled() bool {
	_, err := schtasks("/Query", "/TN", taskName)
	return err == nil
}

func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func setStartup(on bool) error {
	if !on {
		if out, err := schtasks("/Delete", "/TN", taskName, "/F"); err != nil && startupEnabled() {
			return errors.New(strings.TrimSpace(string(out)))
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	u, err := user.Current()
	if err != nil {
		return err
	}
	task := `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Starts Bandwidth Pilot in the notification area when you sign in.</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + xmlText(u.Username) + `</UserId>
      <Delay>PT5S</Delay>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + xmlText(u.Username) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <AllowHardTerminate>true</AllowHardTerminate>
    <Priority>5</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlText(exe) + `</Command>
      <Arguments>--tray</Arguments>
      <WorkingDirectory>` + xmlText(filepath.Dir(exe)) + `</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`
	// schtasks wants the XML file in UTF-16 with a byte-order mark.
	u16 := utf16.Encode([]rune(task))
	buf := make([]byte, 2+2*len(u16))
	buf[0], buf[1] = 0xFF, 0xFE
	for i, c := range u16 {
		buf[2+2*i], buf[3+2*i] = byte(c), byte(c>>8)
	}
	f, err := os.CreateTemp("", "bandwidthpilot-task-*.xml")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := f.Write(buf); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if out, err := schtasks("/Create", "/TN", taskName, "/XML", path, "/F"); err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}
