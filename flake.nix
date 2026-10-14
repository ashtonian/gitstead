{
  description = "gitstead: clone, sync and back up git repos across accounts, and run coding agents in workspaces";

  outputs = { self }: {
    # home-manager module: programs.gitstead. Give it the gitstead package;
    # see nix/home-manager.nix and the README's Nix section.
    homeManagerModules.default = import ./nix/home-manager.nix;
    homeManagerModules.gitstead = self.homeManagerModules.default;
  };
}
