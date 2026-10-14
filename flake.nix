{
  description = "gitten: find, clone and keep in sync every repo you work on, then launch coding agents on them side by side";

  outputs = { self }: {
    # home-manager module: programs.gitten. Give it the gitten package;
    # see nix/home-manager.nix and the README's Nix section.
    homeManagerModules.default = import ./nix/home-manager.nix;
    homeManagerModules.gitten = self.homeManagerModules.default;
  };
}
