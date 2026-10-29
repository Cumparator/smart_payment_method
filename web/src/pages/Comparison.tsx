import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer } from 'recharts';

const mockData = [
  { time: '10:00', cheapest: 85, expected_cost: 98 },
  { time: '10:05', cheapest: 82, expected_cost: 97 },
  { time: '10:10', cheapest: 40, expected_cost: 95 }, // Авария дешёвого канала
  { time: '10:15', cheapest: 45, expected_cost: 96 },
];

export default function Comparison() {
  return (
    <div>
      <div className="flex justify-between items-center mb-6">
        <h2 className="text-2xl font-bold">Сравнение стратегий</h2>
        <button className="bg-blue-600 text-white px-4 py-2 rounded hover:bg-blue-700">
          ▶ Запустить эксперимент (30 seeds)
        </button>
      </div>

      <div className="bg-white p-6 rounded-lg shadow-sm mb-6 h-80">
        <h3 className="text-lg font-semibold mb-4">Успешность платежей во времени (%)</h3>
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={mockData}>
            <CartesianGrid strokeDasharray="3 3" />
            <XAxis dataKey="time" />
            <YAxis domain={[0, 100]} />
            <Tooltip />
            <Legend />
            <Line type="monotone" dataKey="cheapest" stroke="#ef4444" name="Самый дешёвый" strokeWidth={2} />
            <Line type="monotone" dataKey="expected_cost" stroke="#22c55e" name="Ожидаемая цена" strokeWidth={2} />
          </LineChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}