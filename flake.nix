{
  description = "gitten: clone and keep in sync every repo you work on, across orgs, accounts and forges";

  outputs = { self }: {
    # home-manager module: programs.gitten. Give it the gitten package;
    # see nix/home-manager.nix and the README's Nix section.
    homeManagerModules.default = import ./nix/home-manager.nix;
    homeManagerModules.gitten = self.homeManagerModules.default;
  };
}
