import { RouterProvider, useRouter, matchProject, matchRepo, matchPRReview } from "./router";
import { ProjectsPage } from "./pages/ProjectsPage";
import { ProjectPage } from "./pages/ProjectPage";
import { RepoPage } from "./pages/RepoPage";
import { PRReviewPage } from "./pages/PRReviewPage";
import { GlobalPage } from "./pages/GlobalPage";
import { AutomationsPage } from "./pages/AutomationsPage";
import { RunLogPage } from "./pages/RunLogPage";
import { ToastProvider } from "./components/Toasts";
import { UpdateBanner } from "./components/UpdateBanner";
import { useUpdateCheck } from "./hooks/useUpdateCheck";

function Routes() {
  const { path } = useRouter();

  const projectId = matchProject(path);
  if (projectId) return <ProjectPage id={projectId} />;
  const pr = matchPRReview(path);
  if (pr) return <PRReviewPage repoId={pr.repoId} number={pr.number} />;
  const repoId = matchRepo(path);
  if (repoId) return <RepoPage id={repoId} />;
  if (path === "/global") return <GlobalPage />;
  if (path === "/automations/runs") return <RunLogPage />;
  if (path === "/automations") return <AutomationsPage />;
  return <ProjectsPage />;
}

export function App() {
  // Checked once here (persistent across client-side navigation) so the banner
  // shows on every page and a long-lived tab keeps polling.
  const update = useUpdateCheck();
  return (
    <ToastProvider>
      <RouterProvider>
        <UpdateBanner status={update} />
        <Routes />
      </RouterProvider>
    </ToastProvider>
  );
}
