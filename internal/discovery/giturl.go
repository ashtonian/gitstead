package discovery

import "strings"

type sshParts struct {
	host, owner, name string
}

// parseGitURL parses scp-like (git@host:owner/name.git) and ssh://
// (ssh://git@host[:port]/owner/name.git) URLs. Other schemes return nil.
func parseGitURL(url string) *sshParts {
	if strings.HasPrefix(url, "ssh://") {
		return parseSSHURL(url)
	}
	if strings.Contains(url, "://") {
		return nil
	}
	if strings.Contains(url, ":") {
		return splitSSH(url)
	}
	return nil
}

// splitSSH parses an scp-like URL: [user@]host:owner/.../name[.git]
func splitSSH(url string) *sshParts {
	if at := strings.Index(url, "@"); at >= 0 {
		url = url[at+1:]
	}
	colon := strings.Index(url, ":")
	if colon < 0 {
		return nil
	}
	host := url[:colon]
	owner, name := splitOwnerName(strings.TrimSuffix(url[colon+1:], ".git"))
	if host == "" || owner == "" || name == "" {
		return nil
	}
	return &sshParts{host: host, owner: owner, name: name}
}

// parseSSHURL parses ssh://[user@]host[:port]/owner/.../name[.git]
func parseSSHURL(url string) *sshParts {
	s := strings.TrimPrefix(url, "ssh://")
	if at := strings.Index(s, "@"); at >= 0 {
		s = s[at+1:]
	}
	slash := strings.Index(s, "/")
	if slash < 0 {
		return nil
	}
	host := s[:slash]
	if c := strings.Index(host, ":"); c >= 0 {
		host = host[:c]
	}
	owner, name := splitOwnerName(strings.TrimSuffix(s[slash+1:], ".git"))
	if host == "" || owner == "" || name == "" {
		return nil
	}
	return &sshParts{host: host, owner: owner, name: name}
}

// splitOwnerName splits owner/name or group/subgroup/name into the leading
// owner path and the final repo name.
func splitOwnerName(path string) (string, string) {
	path = strings.Trim(path, "/")
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return "", ""
	}
	return path[:idx], path[idx+1:]
}
