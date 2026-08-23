package center

import (
	"errors"
	"strings"
)

func siteDomainBase(code, domainSuffix string) (string, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	domainSuffix = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domainSuffix), "."))
	if !siteCodePattern.MatchString(code) || !domainSuffixPattern.MatchString(domainSuffix) {
		return "", errors.New("center: Site requires a valid domain namespace")
	}
	return code + "." + domainSuffix, nil
}

func validateHostnameInSiteNamespace(hostname, code, domainSuffix string) error {
	hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	base, err := siteDomainBase(code, domainSuffix)
	if err != nil {
		return err
	}
	if hostname != base && !strings.HasSuffix(hostname, "."+base) {
		return errors.New("center: publication hostname must belong to its Site domain namespace")
	}
	return nil
}
