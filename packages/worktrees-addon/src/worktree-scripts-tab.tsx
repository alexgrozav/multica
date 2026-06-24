"use client";

import { useEffect, useState } from "react";
import { Save } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Switch } from "@multica/ui/components/ui/switch";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import type { WorkspaceRepo } from "@multica/core/types";
import { useSaveWorktreeConfig, useWorktreeConfig } from "./queries";
import type { WorktreeRepoScript } from "./types";

type ScriptMap = Record<string, { setup: string; run: string; cleanup: string }>;

function emptyScript() {
  return { setup: "", run: "", cleanup: "" };
}

// Settings tab: the workspace-level auto-init toggle plus per-repository
// Setup/Run/Cleanup script editors. Repos come from the workspace; scripts are
// stored by the add-on (keyed by repo URL).
export function WorktreeScriptsTab() {
  const wsId = useWorkspaceId();
  const workspace = useCurrentWorkspace();
  const repos: WorkspaceRepo[] = workspace?.repos ?? [];
  const { data: config } = useWorktreeConfig(wsId);
  const save = useSaveWorktreeConfig(wsId);

  const [autoInit, setAutoInit] = useState(false);
  const [scripts, setScripts] = useState<ScriptMap>({});

  // Seed local state from the loaded config.
  useEffect(() => {
    if (!config) return;
    setAutoInit(config.auto_init);
    const map: ScriptMap = {};
    for (const r of config.repos) {
      map[r.repo_url] = { setup: r.setup, run: r.run, cleanup: r.cleanup };
    }
    setScripts(map);
  }, [config]);

  const scriptFor = (url: string) => scripts[url] ?? emptyScript();

  const setField = (url: string, field: "setup" | "run" | "cleanup", value: string) => {
    setScripts((prev) => ({ ...prev, [url]: { ...scriptFor(url), [field]: value } }));
  };

  const handleSave = async () => {
    const repoScripts: WorktreeRepoScript[] = repos.map((r) => ({
      repo_url: r.url,
      ...scriptFor(r.url),
    }));
    try {
      await save.mutateAsync({ auto_init: autoInit, repos: repoScripts });
      toast.success("Worktree settings saved");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save worktree settings");
    }
  };

  return (
    <div className="space-y-8">
      <section className="space-y-4">
        <h2 className="text-sm font-semibold">Worktrees</h2>

        <Card>
          <CardContent className="space-y-4">
            <div className="flex items-start justify-between gap-4">
              <div className="space-y-0.5">
                <div className="text-sm font-medium">Auto-initialize worktrees on task creation</div>
                <p className="text-xs text-muted-foreground">
                  When on, creating a task initializes a git worktree for every workspace repository so its
                  Setup script runs and Run becomes available.
                </p>
              </div>
              <Switch checked={autoInit} onCheckedChange={setAutoInit} />
            </div>
          </CardContent>
        </Card>

        <div className="space-y-1">
          <h3 className="text-sm font-medium">Repository scripts</h3>
          <p className="text-xs text-muted-foreground">
            Shell scripts run by the local daemon inside each repository&apos;s worktree. Setup runs once after
            creation, Run on demand from the task sidebar, Cleanup when the task is moved to Done.
          </p>
        </div>

        {repos.length === 0 && (
          <p className="text-xs italic text-muted-foreground">
            No repositories configured for this workspace yet.
          </p>
        )}

        {repos.map((repo) => {
          const s = scriptFor(repo.url);
          return (
            <Card key={repo.url}>
              <CardContent className="space-y-3">
                <div className="truncate font-mono text-xs text-muted-foreground" title={repo.url}>
                  {repo.url}
                </div>
                <ScriptField label="Setup" placeholder="npm ci" value={s.setup} onChange={(v) => setField(repo.url, "setup", v)} />
                <ScriptField label="Run" placeholder="npm run dev" value={s.run} onChange={(v) => setField(repo.url, "run", v)} />
                <ScriptField label="Cleanup" placeholder="docker compose down" value={s.cleanup} onChange={(v) => setField(repo.url, "cleanup", v)} />
              </CardContent>
            </Card>
          );
        })}

        <div className="flex justify-end">
          <Button size="sm" onClick={handleSave} disabled={save.isPending}>
            <Save className="h-3 w-3" />
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        </div>
      </section>
    </div>
  );
}

function ScriptField({
  label,
  placeholder,
  value,
  onChange,
}: {
  label: string;
  placeholder: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="space-y-1">
      <label className="text-xs font-medium text-muted-foreground">{label}</label>
      <Textarea
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        rows={3}
        className="font-mono text-xs"
        spellCheck={false}
      />
    </div>
  );
}
