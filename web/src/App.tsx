import { BrowserRouter, Routes, Route, Link } from 'react-router-dom';
import Channels from './pages/Channels';
import Payments from './pages/Payments';
import Comparison from './pages/Comparison';
import Reconciliation from './pages/Reconciliation';

export default function App() {
  return (
    <BrowserRouter>
      <div className="flex h-screen bg-gray-100">
        <nav className="w-64 bg-white shadow-md p-4">
          <h1 className="text-xl font-bold mb-8">Smart Routing</h1>
          <ul className="space-y-4">
            <li><Link to="/" className="text-blue-600 hover:underline">Каналы</Link></li>
            <li><Link to="/payments" className="text-blue-600 hover:underline">Платежи</Link></li>
            <li><Link to="/comparison" className="text-blue-600 hover:underline">Сравнение</Link></li>
            <li><Link to="/reconciliation" className="text-blue-600 hover:underline">Сверка</Link></li>
          </ul>
        </nav>
        <main className="flex-1 p-8 overflow-auto">
          <Routes>
            <Route path="/" element={<Channels />} />
            <Route path="/payments" element={<Payments />} />
            <Route path="/comparison" element={<Comparison />} />
            <Route path="/reconciliation" element={<Reconciliation />} />
          </Routes>
        </main>
      </div>
    </BrowserRouter>
  );
}