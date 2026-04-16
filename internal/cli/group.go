package cli

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var groupCmd = &cobra.Command{
	Use:   "group",
	Short: "Manage groups of identities (families, etc.)",
}

var groupCreateCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a new group",
	Args:  cobra.ExactArgs(1),
	RunE:  runGroupCreate,
}

var groupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all groups",
	RunE:  runGroupList,
}

var groupAddCmd = &cobra.Command{
	Use:   "add [group-name] [identity-names...]",
	Short: "Add one or more identities to a group",
	Args:  cobra.MinimumNArgs(2),
	RunE:  runGroupAdd,
}

var groupRemoveCmd = &cobra.Command{
	Use:   "remove [group-name] [identity-names...]",
	Short: "Remove identities from a group",
	Args:  cobra.MinimumNArgs(2),
	RunE:  runGroupRemove,
}

var groupMembersCmd = &cobra.Command{
	Use:   "members [group-name]",
	Short: "List members of a group",
	Args:  cobra.ExactArgs(1),
	RunE:  runGroupMembers,
}

var groupDeleteCmd = &cobra.Command{
	Use:   "delete [group-name]",
	Short: "Delete a group (keeps the identities)",
	Args:  cobra.ExactArgs(1),
	RunE:  runGroupDelete,
}

func init() {
	groupCmd.AddCommand(groupCreateCmd, groupListCmd, groupAddCmd, groupRemoveCmd, groupMembersCmd, groupDeleteCmd)
	RootCmd.AddCommand(groupCmd)
}

func runGroupCreate(cmd *cobra.Command, args []string) error {
	name := strings.TrimSpace(args[0])
	if name == "" {
		return fmt.Errorf("group name cannot be empty")
	}

	db, err := openDB(getConfig())
	if err != nil {
		return err
	}
	defer db.Close()

	if existing, err := db.FindGroupByName(name); err == nil && existing != nil {
		fmt.Printf("Group %q already exists (id=%d)\n", name, existing.ID)
		return nil
	}

	id, err := db.CreateGroup(name)
	if err != nil {
		return fmt.Errorf("creating group: %w", err)
	}

	fmt.Printf("Created group %q (id=%d)\n", name, id)
	fmt.Printf("Add members: va group add %q <identity> [<identity>...]\n", name)
	return nil
}

func runGroupList(cmd *cobra.Command, args []string) error {
	db, err := openDB(getConfig())
	if err != nil {
		return err
	}
	defer db.Close()

	groups, err := db.ListGroups()
	if err != nil {
		return err
	}

	if len(groups) == 0 {
		fmt.Println("No groups yet. Create one with: va group create <name>")
		return nil
	}

	fmt.Printf("%-4s  %-30s  %s\n", "ID", "Name", "Members")
	fmt.Println("----  ------------------------------  -------")
	for _, g := range groups {
		fmt.Printf("%-4d  %-30s  %d\n", g.ID, g.Name, g.MemberCount)
	}
	return nil
}

func runGroupAdd(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	identityNames := args[1:]

	db, err := openDB(getConfig())
	if err != nil {
		return err
	}
	defer db.Close()

	group, err := db.FindGroupByName(groupName)
	if err != nil {
		return fmt.Errorf("group %q not found. Create it first with: va group create %q", groupName, groupName)
	}

	added := 0
	for _, name := range identityNames {
		identity, err := db.FindIdentityByNameCaseInsensitive(name)
		if err == sql.ErrNoRows || identity == nil {
			fmt.Printf("  skip %-20s  (not found — name a cluster first via va review)\n", name)
			continue
		}
		if err != nil {
			return fmt.Errorf("looking up identity %q: %w", name, err)
		}

		if err := db.AddGroupMember(group.ID, identity.ID); err != nil {
			return fmt.Errorf("adding %q: %w", identity.Name, err)
		}
		fmt.Printf("  add  %s\n", identity.Name)
		added++
	}

	fmt.Printf("\nAdded %d member(s) to %q\n", added, group.Name)
	return nil
}

func runGroupRemove(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	identityNames := args[1:]

	db, err := openDB(getConfig())
	if err != nil {
		return err
	}
	defer db.Close()

	group, err := db.FindGroupByName(groupName)
	if err != nil {
		return fmt.Errorf("group %q not found", groupName)
	}

	removed := 0
	for _, name := range identityNames {
		identity, err := db.FindIdentityByNameCaseInsensitive(name)
		if err == sql.ErrNoRows || identity == nil {
			fmt.Printf("  skip %-20s  (not found)\n", name)
			continue
		}
		if err := db.RemoveGroupMember(group.ID, identity.ID); err != nil {
			return fmt.Errorf("removing %q: %w", identity.Name, err)
		}
		fmt.Printf("  remove  %s\n", identity.Name)
		removed++
	}

	fmt.Printf("\nRemoved %d member(s) from %q\n", removed, group.Name)
	return nil
}

func runGroupMembers(cmd *cobra.Command, args []string) error {
	groupName := args[0]

	db, err := openDB(getConfig())
	if err != nil {
		return err
	}
	defer db.Close()

	group, err := db.FindGroupByName(groupName)
	if err != nil {
		return fmt.Errorf("group %q not found", groupName)
	}

	members, err := db.ListGroupMembers(group.ID)
	if err != nil {
		return err
	}

	if len(members) == 0 {
		fmt.Printf("%q has no members yet.\n", group.Name)
		fmt.Printf("Add some: va group add %q <identity> [<identity>...]\n", group.Name)
		return nil
	}

	fmt.Printf("%s (%d member%s):\n", group.Name, len(members), plural(len(members)))
	for _, m := range members {
		fmt.Printf("  %d  %s\n", m.ID, m.Name)
	}
	return nil
}

func runGroupDelete(cmd *cobra.Command, args []string) error {
	groupName := args[0]

	db, err := openDB(getConfig())
	if err != nil {
		return err
	}
	defer db.Close()

	group, err := db.FindGroupByName(groupName)
	if err != nil {
		return fmt.Errorf("group %q not found", groupName)
	}

	if err := db.DeleteGroup(group.ID); err != nil {
		return fmt.Errorf("deleting group: %w", err)
	}

	fmt.Printf("Deleted group %q (identities were preserved)\n", group.Name)
	return nil
}
