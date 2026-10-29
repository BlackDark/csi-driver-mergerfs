# Operations

## Mount Pod uid

mergerfs runs as `mountPod.uid`/`gid` (default 65532) and reads and writes every branch as that uid, whatever uid the consumer runs as (ADR 0001). Set them to the owner of the branch data, for example `1000` for a media library owned by uid 1000, and run the consumers as the same uid. Branches from other CSI drivers must allow that uid; an rclone volume with `uid`/`gid` set to the same ids and `allow_other` works.

## Upgrades

`helm upgrade` and DaemonSet rollouts restart the node plugin. Running pools keep serving: the new plugin adopts them ([contract](design/mount-pod-contract.md#restart-and-failure)). An adopted Mount Pod keeps its old image until its consumer restarts; new pools use the new image.

0.1.x ran mergerfs inside the node plugin. Upgrading from it restarts the plugin, which breaks the old pools once more: restart their consumer pods after the upgrade. The volume attributes stay compatible except for the removed `driverHint` branch field; hostPath branches now need `allowedHostPaths`.

## Uninstall

Remove the consumers of this driver first. kubelet calls the node plugin to unpublish a terminating pod; with the plugin gone, the pod stays `Terminating`. Reinstalling the chart lets kubelet finish, and the plugin cleans up the orphaned pools.

## Workload healing

`mountPod.healWorkloads.enabled` evicts the consumer of a lost pool, so its controller recreates it with a fresh pool ([contract](design/mount-pod-contract.md#workload-healing)). Evictions honour PodDisruptionBudgets; bare pods get a `HealBlocked` Event instead. Without it, consumers keep returning `ENOTCONN` until someone restarts them.

## Host mount table left by 0.1.x

0.1.x mounted `<kubeletDir>` and `<kubeletDir>/pods` both `Bidirectional`, and every plugin restart stacked another self-mount of `<kubeletDir>/pods` on the host. The current chart does not add more (ADR 0002), but it does not remove the existing ones. Check with:

```bash
awk '$5=="/var/lib/kubelet/pods"' /proc/self/mountinfo | wc -l
```

More than one line means stacked copies. Every mount below them, such as each CSI target, then shows once per copy. They go away with a node reboot, which is the safe way to clear them.
