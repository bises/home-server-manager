import { useEffect, useState } from "react";
import "./App.css";
import ContainerCard from "./components/ContainerCard";

function App() {
  const [services, setServices] = useState([]);
  const [composeFiles, setComposeFiles] = useState([]);
  const [selectedCompose, setSelectedCompose] = useState("all");
  const [containerStatus, setContainerStatus] = useState({});
  const [loading, setLoading] = useState(true);
  const [statusLoading, setStatusLoading] = useState(false);
  const [statusKnown, setStatusKnown] = useState(false);
  const [error, setError] = useState(null);
  const [statusError, setStatusError] = useState(null);

  async function fetchContainerStatus() {
    setStatusLoading(true);
    try {
      const response = await fetch("/api/containers/status");
      const data = await response.json();
      if (data.status !== "success") {
        throw new Error(data.message || "Could not read container status");
      }

      const statusMap = Object.fromEntries(
        data.containers.map((container) => [
          `${container.Service}|${container.compose_file}`,
          container,
        ])
      );
      setContainerStatus(statusMap);
      setStatusKnown(true);
      setStatusError(null);
    } catch (err) {
      setStatusKnown(false);
      setStatusError(err.message);
    } finally {
      setStatusLoading(false);
    }
  }

  useEffect(() => {
    async function initializeApp() {
      try {
        const response = await fetch("/api/services");
        const data = await response.json();
        if (data.status !== "success") {
          throw new Error(data.message || "Could not read Compose services");
        }
        setServices(data.services);
        setComposeFiles(data.compose_files);
        await fetchContainerStatus();
      } catch (err) {
        setError(err.message);
      } finally {
        setLoading(false);
      }
    }
    initializeApp();
  }, []);

  const handleContainerAction = async (service, action, composeFile) => {
    const file = encodeURIComponent(composeFile);
    const name = encodeURIComponent(service);
    try {
      const response = await fetch(`/api/containers/${action}/${name}?compose_file=${file}`, {
        method: "POST",
      });
      const data = await response.json();
      if (data.status !== "success") {
        throw new Error(data.message || "The action could not be completed");
      }
    } catch (err) {
      alert(err.message);
    } finally {
      await fetchContainerStatus();
    }
  };

  const filteredServices = selectedCompose === "all"
    ? services
    : services.filter((service) => service.compose_file === selectedCompose);

  const groupedServices = filteredServices.reduce((groups, service) => {
    const key = service.compose_file;
    groups[key] = (groups[key] || []).concat(service);
    return groups;
  }, Object.create(null));

  return (
    <div className="App">
      <header className="App-header">
        <h1>🐳 Home Server Manager</h1>
        <p>Manage your Docker containers</p>
      </header>
      {loading ? (
        <div className="skeleton-grid" aria-label="Loading Compose services">
          {[1, 2, 3].map((key) => <div className="skeleton-card" key={key} />)}
        </div>
      ) : error ? (
        <div className="error" role="alert">
          <p>{error}</p>
          <button className="btn-refresh-status" onClick={() => window.location.reload()}>
            Reload Page
          </button>
        </div>
      ) : (
        <>
          <div className="compose-selector">
            <label htmlFor="compose-file">Compose file</label>
            <select
              id="compose-file"
              value={selectedCompose}
              onChange={(e) => setSelectedCompose(e.target.value)}
            >
              <option value="all">All ({services.length} services)</option>
              {composeFiles.map((file) => (
                <option key={file.path} value={file.path}>{file.display_name}</option>
              ))}
            </select>
          </div>
          <div className="status-control">
            <button
              className="btn-refresh-status"
              onClick={fetchContainerStatus}
              disabled={statusLoading}
            >
              {statusLoading ? "Refreshing..." : "🔄 Refresh Status"}
            </button>
            {statusError && <p role="alert">{statusError}</p>}
          </div>
          <main className="container-grid">
            {filteredServices.length === 0 ? (
              <div className="empty-state"><p>No services found</p></div>
            ) : (
              Object.entries(groupedServices).map(([composeFile, serviceList]) => (
                <div key={composeFile} className="compose-group">
                  <h3 className="compose-group-title">{serviceList[0].display_name}</h3>
                  <div className="container-grid">
                    {serviceList.map(({ service }) => {
                      const key = `${service}|${composeFile}`;
                      return (
                        <ContainerCard
                          key={key}
                          service={service}
                          composeFile={composeFile}
                          status={statusKnown ? containerStatus[key] : undefined}
                          statusKnown={statusKnown}
                          onAction={handleContainerAction}
                        />
                      );
                    })}
                  </div>
                </div>
              ))
            )}
          </main>
        </>
      )}
    </div>
  );
}

export default App;
