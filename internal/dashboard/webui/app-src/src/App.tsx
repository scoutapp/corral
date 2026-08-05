import { RouterProvider, useRouter, matchProject } from "./router";
import { ProjectsPage } from "./pages/ProjectsPage";
import { ProjectPage } from "./pages/ProjectPage";
import { GlobalPage } from "./pages/GlobalPage";
import { ToastProvider } from "./components/Toasts";

function Routes() {
  const { path } = useRouter();

  const projectId = matchProject(path);
  if (projectId) return <ProjectPage id={projectId} />;
  if (path === "/global") return <GlobalPage />;
  return <ProjectsPage />;
}

export function App() {
  return (
    <ToastProvider>
      <RouterProvider>
        <Routes />
      </RouterProvider>
    </ToastProvider>
  );
}
