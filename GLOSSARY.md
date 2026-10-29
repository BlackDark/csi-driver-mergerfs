# Glossary

**Pool**: One mergerfs union of branches, published at one target of one pod.

**Branch**: One directory in a Pool, with a mode (`RW`, `RO`, `NC`). Either a volume of the consumer pod or a host path below an allowed root.

**Consumer**: The pod that mounts a Pool. Its volumes can be branches.

**Allowed Host Path**: An admin-set host path root (`allowedHostPaths`) that hostPath branches must be below.

**Mount Pod**: The unprivileged pod that runs one Pool's mergerfs, in the driver namespace.

**Pool Spec**: A volume's parsed attributes: its branches and mergerfs options.

**Fusermount Shim**: Our `mergerfs-fusermount`, which mergerfs runs instead of upstream's setuid helper. It gets the FUSE fd from the node plugin instead of mounting.

**Handoff**: The node plugin mounting a Pool at the shim's request and passing the FUSE fd over the Pool's unix socket.

**Adoption**: A restarted node plugin taking over the running Mount Pods on its node, from the mount table and the Mount Pod list.

**Mount Lost**: A Mount Pod that stopped while the node plugin did not stop it. Its Pool returns `ENOTCONN` until the Consumer restarts. The plugin emits a `MountLost` Event.

**Heal**: Opt-in eviction of a controller-owned Consumer after Mount Lost, so its controller recreates it with a fresh Pool.

**Unrestricted Options**: `allowAllOptions`, an admin-only opt-in on PVs and StorageClasses that accepts mergerfs options outside the allowlist.

**Inline, Static, Dynamic Volume**: A volume in the pod spec, a hand-written PV, or a PV from a StorageClass.
