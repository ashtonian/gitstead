{
  description = "git-mgr: clone, sync and back up git repos across accounts, and run coding agents in workspaces";

  outputs = { self }: {
    # home-manager module: programs.git-mgr. Give it the git-mgr package;
    # see nix/home-manager.nix and the README's Nix section.
    homeManagerModules.default = import ./nix/home-manager.nix;
    homeManagerModules.git-mgr = self.homeManagerModules.default;
  };
}
