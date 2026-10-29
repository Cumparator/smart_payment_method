import { useState } from 'react';
import { Activity, AlertCircle, CheckCircle } from 'lucide-react';

const mockChannels = [
  { id: 'BANK_A', name: 'Банк А (Дешёвый)', status: 'OPEN', load: '85%', latency: '2.1s' },
  { id: 'BANK_B', name: 'Банк Б (Надёжный)', status: 'CLOSED', load: '30%', latency: '0.3s' },
];

export default function Channels() {
  const [channels, setChannels] = useState(mockChannels);

  const simulateCrash = (id: string) => {
    setChannels(prev => prev.map(ch => 
      ch.id === id ? { ...ch, status: 'OPEN', load: '100%', latency: '5.0s+' } : ch
    ));
  };

  return (
    <div>
      <h2 className="text-2xl font-bold mb-6">Состояние каналов (Circuit Breaker)</h2>
      <div className="grid grid-cols-2 gap-4">
        {channels.map(ch => (
          <div key={ch.id} className="bg-white p-6 rounded-lg shadow-sm border">
            <div className="flex justify-between items-center mb-4">
              <h3 className="text-lg font-semibold">{ch.name}</h3>
              {ch.status === 'CLOSED' ? (
                <span className="flex items-center text-green-600"><CheckCircle className="w-5 h-5 mr-1"/> CLOSED (Здоров)</span>
              ) : (
                <span className="flex items-center text-red-600"><AlertCircle className="w-5 h-5 mr-1"/> OPEN (Авария)</span>
              )}
            </div>
            <p className="mb-2">Текущая загрузка: <span className="font-mono">{ch.load}</span></p>
            <p className="mb-4">Задержка: <span className="font-mono">{ch.latency}</span></p>
            <button 
              onClick={() => simulateCrash(ch.id)}
              className="bg-red-50 text-red-600 px-4 py-2 rounded border border-red-200 hover:bg-red-100"
            >
              Эмулировать аварию
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}