# home-manager module for git-mgr: the package, its config, and named
# background syncs as systemd user timers (Linux) or launchd agents (macOS).
#
# `git-mgr install-agent` refuses a binary in /nix/store, which moves on every
# upgrade and is garbage-collected after; here the units are rebuilt with the
# package on each switch, so the store path is the right one. The units match
# what install-agent writes: `git-mgr sync --quiet` plus the schedule's
# selection, every interval, at nice 10 with idle I/O, and on Linux with
# Persistent=true so a run missed while the machine was off happens at boot.
{ config, lib, pkgs, ... }:

let
  cfg = config.programs.git-mgr;
  yaml = pkgs.formats.yaml { };
  inherit (lib) mkOption mkEnableOption mkIf mkMerge types;

  # An interval as git-mgr schedules it: minutes dividing an hour, or hours
  # dividing a day.
  parseInterval = name: interval:
    let
      m = builtins.match "([0-9]+)(m|h)" interval;
      n = if m == null then 0 else lib.toInt (builtins.elemAt m 0);
      unit = if m == null then "" else builtins.elemAt m 1;
      ok = (unit == "m" && n >= 1 && n < 60 && lib.mod 60 n == 0)
        || (unit == "h" && n >= 1 && n <= 24 && lib.mod 24 n == 0);
    in
    if ok then { inherit n unit; seconds = if unit == "m" then n * 60 else n * 3600; }
    else throw "programs.git-mgr.schedules.${name}.interval: ${interval} can't be scheduled exactly; use one that divides an hour or a day (5m, 10m, 15m, 20m, 30m, 1h, 2h, 3h, 4h, 6h, 8h, 12h, 24h)";

  onCalendar = i:
    if i.unit == "m" then "*-*-* *:00/${toString i.n}:00"
    else if i.n < 24 then "*-*-* 00/${toString i.n}:00:00"
    else "*-*-* 00:00:00";

  # The unit or agent name: git-mgr for the default schedule, else
  # git-mgr-<name>, as install-agent names them.
  unitName = name: if name == "default" then "git-mgr" else "git-mgr-${name}";

  globalArgs =
    lib.optionals (cfg.configFile != null) [ "--config" cfg.configFile ]
    ++ lib.optionals (cfg.stateFile != null) [ "--state" cfg.stateFile ];

  syncArgs = s:
    lib.optionals (s.repos != null) [ "--repos" s.repos ]
    ++ lib.optionals (s.tag != null) [ "--tag" s.tag ]
    ++ lib.optionals (s.set != null) [ "--set" s.set ]
    ++ s.extraArgs;

  argv = s: [ "${cfg.package}/bin/git-mgr" ] ++ globalArgs ++ [ "sync" "--quiet" ] ++ syncArgs s;

  environment = s:
    { PATH = lib.makeBinPath ([ cfg.package ] ++ cfg.path); }
    // lib.optionalAttrs (cfg.sshAuthSock != null) { SSH_AUTH_SOCK = cfg.sshAuthSock; }
    // s.environment;

  # Each word quoted, so paths and globs stay one argument.
  systemdQuote = w: "\"" + lib.replaceStrings [ "\\" "\"" ] [ "\\\\" "\\\"" ] w + "\"";
  # In ExecStart, % is also escaped: systemd would read it as a specifier.
  systemdArgv = words: lib.concatMapStringsSep " " (w: systemdQuote (lib.replaceStrings [ "%" ] [ "%%" ] w)) words;

  scheduleType = types.submodule {
    options = {
      interval = mkOption {
        type = types.str;
        default = "15m";
        example = "30m";
        description = "How often to sync: minutes dividing an hour, or hours dividing a day.";
      };
      repos = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "github.com/acme/*";
        description = "Only sync repos whose key matches this glob.";
      };
      tag = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Only sync repos with this tag.";
      };
      set = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Only sync repos in this named set.";
      };
      extraArgs = mkOption {
        type = types.listOf types.str;
        default = [ ];
        example = [ "--no-discover" ];
        description = "More flags for git-mgr sync.";
      };
      environment = mkOption {
        type = types.attrsOf types.str;
        default = { };
        description = "More environment for the sync, such as a token variable a provider's token_env names.";
      };
    };
  };
in
{
  options.programs.git-mgr = {
    enable = mkEnableOption "git-mgr, which clones, syncs and backs up git repos, and runs coding agents in workspaces";

    package = mkOption {
      type = types.package;
      description = "The git-mgr package.";
    };

    settings = mkOption {
      inherit (yaml) type;
      default = { };
      example = lib.literalExpression ''
        {
          base_dir = "~/projects";
          layout = "owner/name";
          accounts = [ { name = "personal"; hosts = [ "github.com" ]; owners = [ "me" ]; } ];
        }
      '';
      description = ''
        git-mgr's config, written to ~/.config/git-mgr/config.yaml; see
        config.example.yaml. Empty leaves the file alone. The file is then
        read-only, so commands that edit it (setup, ws create --save) fail:
        change it here instead.
      '';
    };

    configFile = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = "Config file the schedules use; null for git-mgr's default, ~/.config/git-mgr/config.yaml.";
    };

    stateFile = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = "State file the schedules use; null for git-mgr's default, ~/.local/share/git-mgr/state.yaml.";
    };

    path = mkOption {
      type = types.listOf types.package;
      default = [ pkgs.git pkgs.openssh ];
      defaultText = lib.literalExpression "[ pkgs.git pkgs.openssh ]";
      example = lib.literalExpression "[ pkgs.git pkgs.openssh pkgs.gh ]";
      description = ''
        Programs the background sync finds on PATH: git, ssh, and whatever
        your config runs, such as gh for a token_command or a credential
        helper.
      '';
    };

    sshAuthSock = mkOption {
      type = types.nullOr types.str;
      default = null;
      example = "%t/ssh-agent";
      description = ''
        SSH agent socket the background sync uses; null leaves it to the
        service manager's environment. On Linux, systemd specifiers work
        (%t is the user's runtime directory).
      '';
    };

    logFile = mkOption {
      type = types.str;
      default = "${config.home.homeDirectory}/.local/share/git-mgr/git-mgr.log";
      defaultText = lib.literalExpression ''"''${config.home.homeDirectory}/.local/share/git-mgr/git-mgr.log"'';
      description = "Where launchd agents write sync output; systemd logs to the journal.";
    };

    schedules = mkOption {
      type = types.attrsOf scheduleType;
      default = { };
      example = lib.literalExpression ''
        {
          default = { interval = "15m"; };
          work = { interval = "5m"; tag = "work"; };
        }
      '';
      description = ''
        Named background syncs. git-mgr doctor checks only the schedules
        install-agent recorded, so these show there as not installed; check
        them with systemctl --user list-timers or launchctl list.
      '';
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      home.packages = [ cfg.package ];
      xdg.configFile."git-mgr/config.yaml" = mkIf (cfg.settings != { }) {
        source = yaml.generate "git-mgr-config.yaml" cfg.settings;
      };
    }

    (mkIf pkgs.stdenv.hostPlatform.isLinux {
      systemd.user.services = lib.mapAttrs' (name: s: lib.nameValuePair (unitName name) {
        Unit.Description = "git-mgr sync (${name})";
        Service = {
          Type = "oneshot";
          ExecStart = systemdArgv (argv s);
          # Specifiers such as %t expand here, as sshAuthSock documents.
          Environment = lib.mapAttrsToList (k: v: systemdQuote "${k}=${v}") (environment s);
          Nice = 10;
          IOSchedulingClass = "idle";
        };
      }) cfg.schedules;

      systemd.user.timers = lib.mapAttrs' (name: s: lib.nameValuePair (unitName name) {
        Unit.Description = "Run git-mgr sync (${name}) every ${s.interval}";
        Timer = {
          OnCalendar = onCalendar (parseInterval name s.interval);
          Persistent = true;
        };
        Install.WantedBy = [ "timers.target" ];
      }) cfg.schedules;
    })

    (mkIf pkgs.stdenv.hostPlatform.isDarwin {
      launchd.agents = lib.mapAttrs' (name: s: lib.nameValuePair (unitName name) {
        enable = true;
        config = {
          ProgramArguments = argv s;
          EnvironmentVariables = environment s;
          StartInterval = (parseInterval name s.interval).seconds;
          RunAtLoad = true;
          ProcessType = "Background";
          LowPriorityIO = true;
          Nice = 10;
          StandardOutPath = cfg.logFile;
          StandardErrorPath = cfg.logFile;
        };
      }) cfg.schedules;
    })
  ]);
}
