package util

import (
	"fmt"
	"strconv"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/vault-plugin-secrets-ad/plugin/client"
)

func NewSecretsClient(logger hclog.Logger) *SecretsClient {
	return &SecretsClient{adClient: client.NewClient(logger),
		Logger: logger}
}

// SecretsClient wraps a *activeDirectory.activeDirectoryClient to expose just the common convenience methods needed by the ad secrets backend.
type SecretsClient struct {
	adClient *client.Client
	Logger   hclog.Logger
}

func (c *SecretsClient) Get(conf *client.ADConf, serviceAccountName string) (*client.Entry, error) {
	filters := map[*client.Field][]string{
		client.FieldRegistry.UserPrincipalName: {serviceAccountName},
	}

	entries, err := c.adClient.Search(conf, conf.UserDN, filters)
	if err != nil {
		return nil, err
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("unable to find service account named %s in active directory, searches are case sensitive", serviceAccountName)
	}
	if len(entries) > 1 {
		return nil, fmt.Errorf("expected one matching service account, but received %+v", entries)
	}
	return entries[0], nil
}

func (c *SecretsClient) GetPasswordLastSet(conf *client.ADConf, serviceAccountName string) (time.Time, error) {
	entry, err := c.Get(conf, serviceAccountName)
	if err != nil {
		return time.Time{}, err
	}

	values, found := entry.Get(client.FieldRegistry.PasswordLastSet)
	if !found {
		return time.Time{}, fmt.Errorf("%+v lacks a PasswordLastSet field", entry)
	}

	if len(values) != 1 {
		return time.Time{}, fmt.Errorf("expected only one value for PasswordLastSet, but received %s", values)
	}

	ticks := values[0]
	if ticks == "0" {
		// password has never been rolled in Active Directory, only created
		return time.Time{}, nil
	}

	t, err := client.ParseTicks(ticks)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

func (c *SecretsClient) UpdatePassword(conf *client.ADConf, serviceAccountName string, newPassword string) error {
	filters := map[*client.Field][]string{
		client.FieldRegistry.UserPrincipalName: {serviceAccountName},
	}
	return c.adClient.UpdatePassword(conf, conf.UserDN, filters, newPassword)
}

func (c *SecretsClient) UpdateRootPassword(conf *client.ADConf, bindDN string, newPassword string) error {
	filters := map[*client.Field][]string{
		client.FieldRegistry.DistinguishedName: {bindDN},
	}
	// Here, use the binddn as the base for the search tree, since it actually may live
	// in a separate location from the users it's managing. For example, suppose the root
	// user was in a "Security" OU, while the users whose passwords were being managed were
	// in a separate, non-overlapping "Accounting" OU. We wouldn't want to search the
	// accounting team to rotate the security user's password, we'd want to search the
	// security team.
	return c.adClient.UpdatePassword(conf, conf.BindDN, filters, newPassword)
}

// DisableAccount if account is not already disabled by updating the UserAccountControl attribute
func (c *SecretsClient) DisableAccount(conf *client.ADConf, serviceAccountName string) error {
	uacFlag, err := c.getUserAccountControl(conf, serviceAccountName)
	if err != nil {
		return err
	}
	if !uacFlag.Has(client.ACCOUNTDISABLE) {
		c.Logger.Debug(fmt.Sprintf("Account before disabled - UAC for %s, %x", serviceAccountName, uacFlag))
		uacFlag.Add(client.ACCOUNTDISABLE)
		return c.updateUAC(conf, serviceAccountName, uacFlag)
	} else {
		c.Logger.Debug(fmt.Sprintf("Account already disabled - UAC for %s, %x", serviceAccountName, uacFlag))
		return nil
	}

}

// EnableAccount if account is not already enabled by updating the UserAccountControl attribute
func (c *SecretsClient) EnableAccount(conf *client.ADConf, serviceAccountName string) error {
	uacFlag, err := c.getUserAccountControl(conf, serviceAccountName)
	if err != nil {
		return err
	}
	if uacFlag.Has(client.ACCOUNTDISABLE) {
		c.Logger.Debug(fmt.Sprintf("Account before enable - UAC for %s, %x", serviceAccountName, uacFlag))
		uacFlag.Clear(client.ACCOUNTDISABLE)
		return c.updateUAC(conf, serviceAccountName, uacFlag)
	} else {
		c.Logger.Debug(fmt.Sprintf("Account already enabled - UAC for %s, %x", serviceAccountName, uacFlag))
		return nil
	}
}

// Update the UserAccountControl attribute
func (c *SecretsClient) updateUAC(conf *client.ADConf, serviceAccountName string, uac client.Bits) error {
	c.Logger.Debug(fmt.Sprintf("Account updated for %s, %x", serviceAccountName, uac))
	uacInt := uint64(uac)

	filters := map[*client.Field][]string{
		client.FieldRegistry.UserPrincipalName: {serviceAccountName},
	}
	newValues := map[*client.Field][]string{
		client.FieldRegistry.UserAccountControl: {strconv.FormatUint(uacInt, 10)},
	}
	return c.adClient.UpdateEntry(conf, conf.UserDN, filters, newValues)
}

// Return the UserAccountControl attribute from Active Directory
func (c *SecretsClient) getUserAccountControl(conf *client.ADConf, serviceAccountName string) (client.Bits, error) {
	entry, err := c.Get(conf, serviceAccountName)
	if err != nil {
		return client.Bits(0), err
	}
	values, found := entry.Get(client.FieldRegistry.UserAccountControl)
	if !found {
		return client.Bits(0), fmt.Errorf("%+v lacks a UserAccountControl field", entry)
	}

	if len(values) != 1 {
		return client.Bits(0), fmt.Errorf("expected only one value for UserAccountControl, but received %s", values)
	}

	val := values[0]
	c.Logger.Debug(fmt.Sprintf("Get UAC for %s, %s", serviceAccountName, val))
	uac64, err := strconv.ParseUint(val, 10, 32)
	return client.Bits(uint32(uac64)), nil
}
