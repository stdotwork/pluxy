// The agent clients' setup pages, in the window's page order. Each names the optional tool it
// drives, and Beginner Mode offers the ones whose tool is installed or offered by a registry.

import { claudeCodePage } from "./claudeCode";
import { claudeDesktopPage } from "./claudeDesktop";
import { codexPage } from "./codex";
import type { AgentPage } from "./easyMode";
import { grokPage } from "./grok";
import { hermesPage } from "./hermes";
import { ompPage } from "./omp";
import { opencodePage } from "./opencode";
import { piPage } from "./pi";

export const agentPages = (): AgentPage[] => [
  claudeCodePage(),
  codexPage(),
  claudeDesktopPage(),
  opencodePage(),
  ompPage(),
  grokPage(),
  hermesPage(),
  piPage(),
];
