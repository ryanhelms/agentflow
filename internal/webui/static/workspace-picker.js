"use strict";

async function showWorktrees() {
  const message = document.getElementById("picker-message");
  try {
    const remember = localStorage.getItem("devflow.browser");
    if (remember) {
      const response = await fetch("/_devflow/worktrees", {
        headers: {Authorization: "Bearer " + remember}, cache: "no-store", redirect: "error"
      });
      if (response.ok) {
        const worktrees = await response.json();
        if (!worktrees.length) {
          message.textContent = "The registered worktrees moved or were removed. Open a current checkout from Agent Commander.";
          return;
        }
        for (const worktree of worktrees) {
          const item = document.createElement("li");
          const link = document.createElement("a");
          link.href = worktree.path + location.hash;
          link.textContent = worktree.root;
          item.append(link);
          document.getElementById("worktrees").append(item);
        }
        message.hidden = true;
        return;
      }
    }
    message.textContent = "In Agent Commander, ask to open devflow for the worktree you want. You can also run devflow ui from that checkout.";
  } catch {
    message.textContent = "Open devflow from Agent Commander or run devflow ui in the worktree to reconnect.";
  }
}

showWorktrees();
