import { useEffect, useState } from "react";
import "./App.css";
import ContainerCard from "./components/ContainerCard";

function App() {
  const [services, setServices] = useState([]);
  const [containerStatus, setContainerStatus] = useState({});
  const [loading, setLoading] = useState(true);
  const [statusLoading, setStatusLoading] = useState(false);
  const [error, setError] = useState(null);
  const [composeFiles, setComposeFiles] = useState([]);
  const [selectedCompose, setSelectedCompose] = useState("all");
  const [openclawStatus, setOpenclawStatus] = useState(null);
  const [showOpenClaw, setShowOpenClaw] = useState(false);

  useEffect(() => {
    const initializeApp = async () => {
      await fetchComposeFiles();
      await fetchServices();
      await fetchContainerStatus();
    };
    initializeApp();
  }, []);

  const fetchComposeFiles = async () => {
    try {
      const response = await fetch("/api/compose-files");
      const data = await response.json();
      if (data.status === "success") {
        setComposeFiles(data.compose_files || []);
      }
    } catch (err) {
      console.error("Error fetching compose files:", err);
    }
  };

  const fetchServices = async () => {
    try {
      setLoading(true);
      const response = await fetch("/api/services");
      const data = await response.json();

      if (data.status === "success") {
        setServices(data.services || []);
        setError(null);
      } else {
        setError(data.message);
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  };

  const fetchContainerStatus = async () => {
    try {
      setStatusLoading(true);
      const response = await fetch("/api/containers/status");
      const data = await response.json();

      if (data.status === "success") {
        // Create a map of service name to container data
        const statusMap = {};
        data.containers.forEach((container) => {
          const key = `${container.Service}|${container.compose_file}`;
          statusMap[key] = container;
        });
        setContainerStatus(statusMap);
      }
    } catch (err) {
      console.error("Error fetching container status:", err);
    } finally {
      setStatusLoading(false);
    }
  };

  const fetchOpenClawStatus = async () => {
    try {
      const response = await fetch("/api/openclaw/status");
      const data = await response.json();
      if (data.status === "success") {
        setOpenclawStatus(data.openclaw);
      }
    } catch (err) {
      console.error("Error fetching OpenClaw status:", err);
    }
  };

  const handleContainerAction = async (service, action, composeFile) => {
    try {
      const url = `/api/containers/${action}/${service}${composeFile ? `?compose_file=${encodeURIComponent(composeFile)}` : ''}`;
      const response = await fetch(url, {
        method: "POST",
      });
      const data = await response.json();

      if (data.status === "success") {
        // Refresh status after action
        setTimeout(() => {
          fetchContainerStatus();
        }, 1000);
      } else {
        console.error(`Failed to ${action} ${service}:`, data.message);
        alert(`Failed to ${action} ${service}: ${data.message}`);
      }
    } catch (err) {
      console.error(`Error performing ${action} on ${service}:`, err);
      alert(`Error: ${err.message}`);
    }
  };

  if (loading) {
    return (
      <div className="App">
        <header className="App-header">
          <h1>🐳 Home Server Manager</h1>
          <p>Manage your Docker containers</p>
        </header>
        <div className="skeleton-grid">
          {[1, 2, 3].map((i) => (
            <div key={i} className="skeleton-card"></div>
          ))}
        </div>
      </div>
    );
  }

  if (error) {
    return (
      <div className="App">
        <header className="App-header">
          <h1>🐳 Home Server Manager</h1>
          <p>Manage your Docker containers</p>
        </header>
        <div className="error">
          <p>⚠️ Error: {error}</p>
          <button
            className="btn-refresh-status"
            onClick={() => window.location.reload()}
            style={{ marginTop: "20px" }}
          >
            Reload Page
          </button>
        </div>
      </div>
    );
  }

  // Filter services by selected compose file
  const filteredServices = selectedCompose === "all" 
    ? services 
    : services.filter(s => s.compose_file === selectedCompose);

  // Group services by compose file for display
  const groupedServices = filteredServices.reduce((acc, service) => {
    const key = service.compose_file || "unknown";
    if (!acc[key]) {
      acc[key] = [];
    }
    acc[key].push(service);
    return acc;
  }, {});

  return (
    <div className="App">
      <header className="App-header">
        <h1>🐳 Home Server Manager</h1>
        <p>Manage your Docker containers</p>
        <button 
          className="btn-openclaw"
          onClick={() => {
            setShowOpenClaw(!showOpenClaw);
            if (!showOpenClaw && !openclawStatus) fetchOpenClawStatus();
          }}
        >
          {showOpenClaw ? "🔙 Back" : "🤖 OpenClaw"}
        </button>
      </header>

      {showOpenClaw ? (
        <div className="openclaw-panel">
          <h2>🤖 OpenClaw Status</h2>
          {openclawStatus ? (
            <pre className="openclaw-info">
              {JSON.stringify(openclawStatus, null, 2)}
            </pre>
          ) : (
            <p>Loading...</p>
          )}
        </div>
      ) : (
        <>
          <div className="compose-selector">
            <label>Compose File: </label>
            <select 
              value={selectedCompose} 
              onChange={(e) => setSelectedCompose(e.target.value)}
            >
              <option value="all">All ({services.length} services)</option>
              {composeFiles.map((cf) => (
                <option key={cf.path} value={cf.path}>
                  {cf.display_name}
                </option>
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
          </div>

          <main className="container-grid">
            {filteredServices.length === 0 ? (
              <div className="empty-state">
                <p>📦 No services found</p>
              </div>
            ) : (
              Object.entries(groupedServices).map(([composeFile, svcList]) => (
                <div key={composeFile} className="compose-group">
                  <h3 className="compose-group-title">
                    {composeFiles.find(cf => cf.path === composeFile)?.display_name || composeFile}
                  </h3>
                  <div className="container-grid">
                    {svcList.map((serviceObj) => {
                      const key = `${serviceObj.service}|${serviceObj.compose_file}`;
                      return (
                        <ContainerCard
                          key={key}
                          service={serviceObj.service}
                          composeFile={serviceObj.compose_file}
                          status={containerStatus[key]}
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
