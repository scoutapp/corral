import { RouterProvider, useRouter, matchProject } from "./router";
import { ProjectsPage } from "./pages/ProjectsPage";
import { ProjectPage } from "./pages/ProjectPage";
import { GlobalPage } from "./pages/GlobalPage";
import { ToastProvider } from "./components/Toasts";
import { UpdateBanner } from "./components/UpdateBanner";
import { useUpdateCheck } from "./hooks/useUpdateCheck";

function Routes() {
  const { path } = useRouter();

  const projectId = matchProject(path);
  if (projectId) return <ProjectPage id={projectId} />;
  if (path === "/global") return <GlobalPage />;
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
